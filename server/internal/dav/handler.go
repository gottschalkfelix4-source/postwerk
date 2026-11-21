// Package dav implements a CalDAV (RFC 4791) and CardDAV (RFC 6352) server
// on top of the store, including sync-collection (RFC 6578), the CTag
// extension and extended MKCOL (RFC 5689).
//
// URL layout (all below /dav):
//
//	/dav/principals/<email>/                  principal
//	/dav/calendars/<email>/                   calendar home
//	/dav/calendars/<email>/<calendar>/        calendar collection
//	/dav/calendars/<email>/<calendar>/<name>  calendar object (.ics)
//	/dav/addressbooks/<email>/...             the same for address books (.vcf)
package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/pim"
	"postwerk/internal/store"
)

// Prefix is the URL path below which the DAV server is mounted.
const Prefix = "/dav"

// MaxObjectSize limits a single calendar object or vCard.
const MaxObjectSize = 10 << 20

// Handler serves CalDAV and CardDAV.
type Handler struct {
	Store *store.Store
	// Authenticate checks Basic credentials (account or app password).
	Authenticate func(ctx context.Context, ip, user, pass string) (*store.Account, error)
	// Client returns the effective client IP and whether the original
	// request used HTTPS (directly or via a trusted proxy).
	Client func(r *http.Request) (ip string, https bool)
	// AllowInsecure permits Basic auth over plain HTTP (tests/development).
	AllowInsecure func() bool
	Log           *slog.Logger
}

// resource kinds addressed by a request path.
type resKind int

const (
	resRoot resKind = iota
	resPrincipals
	resPrincipal
	resHomeParent // /dav/calendars/ or /dav/addressbooks/
	resHome
	resCollection
	resObject
)

type target struct {
	kind     resKind
	collKind string // store.KindCalendar / store.KindAddressBook
	email    string
	slug     string
	name     string
	coll     *store.Collection
}

func homeSegment(kind string) string {
	if kind == store.KindAddressBook {
		return "addressbooks"
	}
	return "calendars"
}

func principalPath(email string) string {
	return Prefix + "/principals/" + url.PathEscape(email) + "/"
}

func homePath(kind, email string) string {
	return Prefix + "/" + homeSegment(kind) + "/" + url.PathEscape(email) + "/"
}

func collPath(c *store.Collection, email string) string {
	return homePath(c.Kind, email) + url.PathEscape(c.Slug) + "/"
}

func objPath(c *store.Collection, email, name string) string {
	return collPath(c, email) + url.PathEscape(name)
}

// parsePath splits a request path into its DAV resource.
func parsePath(p string) (target, bool) {
	p = path.Clean("/" + p)
	if p != Prefix && !strings.HasPrefix(p, Prefix+"/") {
		return target{}, false
	}
	rest := strings.Trim(strings.TrimPrefix(p, Prefix), "/")
	if rest == "" {
		return target{kind: resRoot}, true
	}
	segs := strings.Split(rest, "/")
	switch segs[0] {
	case "principals":
		switch len(segs) {
		case 1:
			return target{kind: resPrincipals}, true
		case 2:
			return target{kind: resPrincipal, email: store.Normalize(segs[1])}, true
		}
		return target{}, false
	case "calendars", "addressbooks":
		t := target{collKind: store.KindCalendar}
		if segs[0] == "addressbooks" {
			t.collKind = store.KindAddressBook
		}
		switch len(segs) {
		case 1:
			t.kind = resHomeParent
		case 2:
			t.kind, t.email = resHome, store.Normalize(segs[1])
		case 3:
			t.kind, t.email, t.slug = resCollection, store.Normalize(segs[1]), segs[2]
		case 4:
			t.kind, t.email, t.slug, t.name = resObject, store.Normalize(segs[1]), segs[2], segs[3]
		default:
			return target{}, false
		}
		return t, true
	}
	return target{}, false
}

const allowMethods = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCOL, MKCALENDAR, MOVE"

func (h *Handler) log() *slog.Logger {
	if h.Log == nil {
		return slog.Default()
	}
	return h.Log
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/caldav", "/.well-known/carddav":
		// RFC 6764: clients continue with PROPFIND current-user-principal.
		http.Redirect(w, r, Prefix+"/", http.StatusMovedPermanently)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodOptions {
		h.options(w)
		return
	}
	ip, https := h.Client(r)
	if !https && (h.AllowInsecure == nil || !h.AllowInsecure()) {
		// Never accept credentials over an unencrypted connection.
		http.Error(w, "CalDAV/CardDAV ist nur über HTTPS erreichbar.", http.StatusForbidden)
		return
	}
	user, pass, okAuth := r.BasicAuth()
	if !okAuth {
		challenge(w)
		return
	}
	acct, err := h.Authenticate(r.Context(), ip, user, pass)
	if err != nil {
		if errors.Is(err, auth.ErrBanned) {
			http.Error(w, "Zu viele fehlgeschlagene Anmeldungen. Bitte später erneut versuchen.", http.StatusTooManyRequests)
			return
		}
		if !errors.Is(err, auth.ErrInvalid) && !errors.Is(err, auth.ErrAccountLocked) {
			h.log().Error("dav authentication failed", "component", "dav", "err", err)
		}
		challenge(w)
		return
	}
	t, ok := parsePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if t.email != "" && t.email != store.Normalize(acct.Email) {
		http.Error(w, "Kein Zugriff auf fremde Kalender oder Adressbücher.", http.StatusForbidden)
		return
	}
	req := &request{h: h, w: w, r: r, acct: acct, t: t}
	if err := req.serve(); err != nil {
		req.fail(err)
	}
}

func challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Postwerk", charset="UTF-8"`)
	http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
}

func (h *Handler) options(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, 3, access-control, calendar-access, addressbook, extended-mkcol")
	w.Header().Set("Allow", allowMethods)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// request carries the state of one authenticated DAV request.
type request struct {
	h    *Handler
	w    http.ResponseWriter
	r    *http.Request
	acct *store.Account
	t    target
}

// httpError is an error with an HTTP status (and optional DAV precondition).
type httpError struct {
	code   int
	msg    string
	cond   string // precondition element (namespace in condNS)
	condNS string
	inner  string
}

func (e *httpError) Error() string { return e.msg }

func errStatus(code int, msg string) error { return &httpError{code: code, msg: msg} }

func errCond(code int, ns, cond, inner string) error {
	return &httpError{code: code, msg: cond, cond: cond, condNS: ns, inner: inner}
}

func (q *request) fail(err error) {
	var he *httpError
	switch {
	case errors.As(err, &he):
		if he.cond != "" {
			writeError(q.w, he.code, name(he.condNS, he.cond), he.inner)
			return
		}
		http.Error(q.w, he.msg, he.code)
	case errors.Is(err, store.ErrNotFound):
		http.Error(q.w, "Nicht gefunden", http.StatusNotFound)
	case errors.Is(err, store.ErrPrecondition):
		http.Error(q.w, "Vorbedingung nicht erfüllt", http.StatusPreconditionFailed)
	case errors.Is(err, errBadXML):
		http.Error(q.w, "Ungültiger XML-Body", http.StatusBadRequest)
	default:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(q.w, "Zu groß", http.StatusRequestEntityTooLarge)
			return
		}
		q.h.log().Error("dav request failed", "component", "dav", "method", q.r.Method, "path", q.r.URL.Path, "err", err)
		http.Error(q.w, "Interner Fehler", http.StatusInternalServerError)
	}
}

func (q *request) ctx() context.Context { return q.r.Context() }

// collection loads the collection addressed by the request.
func (q *request) collection() (*store.Collection, error) {
	if q.t.coll != nil {
		return q.t.coll, nil
	}
	if err := q.h.Store.EnsureDefaultCollections(q.ctx(), q.acct.ID); err != nil {
		return nil, err
	}
	c, err := q.h.Store.GetCollectionBySlug(q.ctx(), q.acct.ID, q.t.collKind, q.t.slug)
	if err != nil {
		return nil, err
	}
	q.t.coll = c
	return c, nil
}

func (q *request) serve() error {
	switch q.r.Method {
	case "PROPFIND":
		return q.propfind()
	case "PROPPATCH":
		return q.proppatch()
	case "REPORT":
		return q.report()
	case http.MethodGet, http.MethodHead:
		return q.get()
	case http.MethodPut:
		return q.put()
	case http.MethodDelete:
		return q.delete()
	case "MKCALENDAR":
		return q.mkcol(true)
	case "MKCOL":
		return q.mkcol(false)
	case "MOVE":
		return q.move()
	}
	q.w.Header().Set("Allow", allowMethods)
	return errStatus(http.StatusMethodNotAllowed, "Methode nicht unterstützt")
}

// ---- GET / HEAD ----

func contentType(o *store.DavObject) string {
	if o.Component == "VCARD" {
		return "text/vcard; charset=utf-8"
	}
	return "text/calendar; charset=utf-8; component=" + o.Component
}

func quoteETag(e string) string { return `"` + e + `"` }

// unquoteETag strips quotes and a weak prefix from an entity tag.
func unquoteETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}

func (q *request) get() error {
	switch q.t.kind {
	case resObject:
		c, err := q.collection()
		if err != nil {
			return err
		}
		o, err := q.h.Store.GetObject(q.ctx(), c, q.t.name)
		if err != nil {
			return err
		}
		h := q.w.Header()
		h.Set("Content-Type", contentType(o))
		h.Set("ETag", quoteETag(o.ETag))
		h.Set("Last-Modified", o.UpdatedAt.UTC().Format(http.TimeFormat))
		h.Set("Content-Length", fmt.Sprint(len(o.Data)))
		if inm := q.r.Header.Get("If-None-Match"); inm != "" && unquoteETag(inm) == o.ETag {
			q.w.WriteHeader(http.StatusNotModified)
			return nil
		}
		q.w.WriteHeader(http.StatusOK)
		if q.r.Method == http.MethodGet {
			_, _ = q.w.Write(o.Data)
		}
		return nil
	case resCollection:
		// Whole collection as one file (subscriptions, quick exports).
		c, err := q.collection()
		if err != nil {
			return err
		}
		objs, err := q.h.Store.ListObjects(q.ctx(), c)
		if err != nil {
			return err
		}
		var body []byte
		if c.Kind == store.KindCalendar {
			datas := make([][]byte, len(objs))
			for i, o := range objs {
				datas[i] = o.Data
			}
			if body, err = pim.Merge(c.Name, datas); err != nil {
				return err
			}
			q.w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		} else {
			var buf bytes.Buffer
			for _, o := range objs {
				buf.Write(bytes.TrimRight(o.Data, "\r\n"))
				buf.WriteString("\r\n")
			}
			body = buf.Bytes()
			q.w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		}
		q.w.Header().Set("ETag", quoteETag(fmt.Sprintf("c%d-%d", c.ID, c.SyncSeq)))
		q.w.WriteHeader(http.StatusOK)
		if q.r.Method == http.MethodGet {
			_, _ = q.w.Write(body)
		}
		return nil
	}
	q.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	q.w.WriteHeader(http.StatusOK)
	if q.r.Method == http.MethodGet {
		_, _ = io.WriteString(q.w, "Postwerk CalDAV/CardDAV\n")
	}
	return nil
}

// ---- PUT ----

func (q *request) put() error {
	if q.t.kind != resObject {
		return errStatus(http.StatusMethodNotAllowed, "PUT nur auf Einträge")
	}
	c, err := q.collection()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errStatus(http.StatusConflict, "Sammlung existiert nicht")
		}
		return err
	}
	if !store.ValidSlug(q.t.name) {
		return errStatus(http.StatusBadRequest, "Ungültiger Name")
	}
	data, err := io.ReadAll(http.MaxBytesReader(q.w, q.r.Body, MaxObjectSize))
	if err != nil {
		return err
	}
	opts := store.PutOptions{}
	if im := q.r.Header.Get("If-Match"); im != "" {
		opts.IfMatch = unquoteETag(im)
		if strings.TrimSpace(im) == "*" {
			opts.IfMatch = "*"
		}
	}
	if strings.TrimSpace(q.r.Header.Get("If-None-Match")) == "*" {
		opts.IfNoneMatch = true
	}
	o, created, err := q.h.Store.PutObject(q.ctx(), c, q.t.name, data, opts)
	if err != nil {
		return putError(c, err)
	}
	q.w.Header().Set("ETag", quoteETag(o.ETag))
	q.w.Header().Set("Last-Modified", o.UpdatedAt.UTC().Format(http.TimeFormat))
	if created {
		q.w.WriteHeader(http.StatusCreated)
	} else {
		q.w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

func putError(c *store.Collection, err error) error {
	cal := c.Kind == store.KindCalendar
	switch {
	case errors.Is(err, store.ErrUIDConflict):
		if cal {
			return errCond(http.StatusConflict, nsCalDAV, "no-uid-conflict", "")
		}
		return errCond(http.StatusConflict, nsCard, "no-uid-conflict", "")
	case errors.Is(err, pim.ErrInvalid):
		if strings.Contains(err.Error(), "not supported") {
			return errCond(http.StatusForbidden, nsCalDAV, "supported-calendar-component", "")
		}
		return errCond(http.StatusForbidden, nsCalDAV, "valid-calendar-data", "")
	case errors.Is(err, pim.ErrInvalidCard):
		return errCond(http.StatusForbidden, nsCard, "valid-address-data", "")
	}
	return err
}

// ---- DELETE ----

func (q *request) delete() error {
	switch q.t.kind {
	case resObject:
		c, err := q.collection()
		if err != nil {
			return err
		}
		ifMatch := ""
		if im := q.r.Header.Get("If-Match"); im != "" {
			ifMatch = unquoteETag(im)
		}
		if err := q.h.Store.DeleteObject(q.ctx(), c, q.t.name, ifMatch); err != nil {
			return err
		}
	case resCollection:
		c, err := q.collection()
		if err != nil {
			return err
		}
		all, err := q.h.Store.ListCollections(q.ctx(), q.acct.ID, c.Kind)
		if err != nil {
			return err
		}
		if len(all) <= 1 {
			return errStatus(http.StatusForbidden, "Die letzte Sammlung kann nicht gelöscht werden")
		}
		if err := q.h.Store.DeleteCollection(q.ctx(), q.acct.ID, c.ID); err != nil {
			return err
		}
	default:
		return errStatus(http.StatusForbidden, "Löschen hier nicht erlaubt")
	}
	q.w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- MKCALENDAR / MKCOL ----

func (q *request) mkcol(calendar bool) error {
	if q.t.kind != resCollection {
		return errStatus(http.StatusForbidden, "Sammlungen nur im Home-Verzeichnis anlegen")
	}
	if calendar && q.t.collKind != store.KindCalendar {
		return errStatus(http.StatusForbidden, "Kalender nur unter /dav/calendars/ anlegen")
	}
	if !store.ValidSlug(q.t.slug) {
		return errStatus(http.StatusBadRequest, "Ungültiger Name")
	}
	body, err := parseBody(q.r)
	if err != nil {
		return err
	}
	c := &store.Collection{AccountID: q.acct.ID, Kind: q.t.collKind, Slug: q.t.slug, Name: q.t.slug}
	var set *node
	if body != nil {
		for _, s := range body.children(nsDAV, "set") {
			if p := s.child(nsDAV, "prop"); p != nil {
				if set == nil {
					set = &node{}
				}
				set.Children = append(set.Children, p.Children...)
			}
		}
	}
	if !calendar && set != nil {
		// Extended MKCOL: the resource type decides what is created.
		if rt := set.child(nsDAV, "resourcetype"); rt != nil {
			isCal, isBook := rt.child(nsCalDAV, "calendar") != nil, rt.child(nsCard, "addressbook") != nil
			switch {
			case isCal && q.t.collKind == store.KindCalendar, isBook && q.t.collKind == store.KindAddressBook:
			default:
				return errCond(http.StatusForbidden, nsDAV, "valid-resourcetype", "")
			}
		}
	} else if !calendar && q.t.collKind == store.KindCalendar {
		return errCond(http.StatusForbidden, nsDAV, "valid-resourcetype", "")
	}
	if set != nil {
		applyCollectionProps(c, set.Children)
		if sc := set.child(nsCalDAV, "supported-calendar-component-set"); sc != nil && c.Kind == store.KindCalendar {
			for _, comp := range sc.children(nsCalDAV, "comp") {
				n := strings.ToUpper(comp.attr("name"))
				if n == "VEVENT" || n == "VTODO" || n == "VJOURNAL" {
					c.Components = append(c.Components, n)
				}
			}
		}
	}
	if err := q.h.Store.EnsureDefaultCollections(q.ctx(), q.acct.ID); err != nil {
		return err
	}
	if err := q.h.Store.CreateCollection(q.ctx(), c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errCond(http.StatusMethodNotAllowed, nsDAV, "resource-must-be-null", "")
		}
		return err
	}
	q.w.Header().Set("Location", collPath(c, q.acct.Email))
	q.w.WriteHeader(http.StatusCreated)
	return nil
}

// applyCollectionProps stores the writable collection properties and
// returns the names it handled.
func applyCollectionProps(c *store.Collection, props []*node) {
	for _, p := range props {
		v := strings.TrimSpace(p.Text)
		switch p.Name {
		case name(nsDAV, "displayname"):
			if v != "" {
				c.Name = v
			}
		case name(nsApple, "calendar-color"):
			if len(v) == 7 || len(v) == 9 {
				if strings.HasPrefix(v, "#") {
					c.Color = v[:7]
				}
			}
		case name(nsCalDAV, "calendar-description"), name(nsCard, "addressbook-description"):
			c.Description = v
		}
	}
}

// ---- MOVE ----

func (q *request) move() error {
	if q.t.kind != resObject {
		return errStatus(http.StatusForbidden, "Nur Einträge können verschoben werden")
	}
	dest, err := url.Parse(q.r.Header.Get("Destination"))
	if err != nil || dest.Path == "" {
		return errStatus(http.StatusBadRequest, "Destination fehlt")
	}
	dt, ok := parsePath(dest.Path)
	if !ok || dt.kind != resObject || dt.collKind != q.t.collKind || dt.email != store.Normalize(q.acct.Email) {
		return errStatus(http.StatusBadGateway, "Ungültiges Ziel")
	}
	if !store.ValidSlug(dt.name) {
		return errStatus(http.StatusBadRequest, "Ungültiger Name")
	}
	from, err := q.collection()
	if err != nil {
		return err
	}
	to, err := q.h.Store.GetCollectionBySlug(q.ctx(), q.acct.ID, dt.collKind, dt.slug)
	if err != nil {
		return errStatus(http.StatusConflict, "Zielsammlung existiert nicht")
	}
	overwrite := !strings.EqualFold(strings.TrimSpace(q.r.Header.Get("Overwrite")), "F")
	created, err := q.h.Store.MoveObject(q.ctx(), from, q.t.name, to, dt.name, overwrite)
	if err != nil {
		return putError(to, err)
	}
	if created {
		q.w.WriteHeader(http.StatusCreated)
	} else {
		q.w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

// lastModified formats a timestamp for getlastmodified.
func lastModified(t time.Time) string { return t.UTC().Format(http.TimeFormat) }
