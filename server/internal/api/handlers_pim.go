package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"postwerk/internal/pim"
	"postwerk/internal/store"
)

// Calendars, events, tasks and contacts for the web interface. The data is
// shared with CalDAV/CardDAV (see internal/dav).

// pimErr maps storage and validation errors to API errors.
func pimErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pim.ErrInvalid), errors.Is(err, pim.ErrInvalidCard):
		return invalid("Ungültige Daten: " + err.Error())
	case errors.Is(err, store.ErrPrecondition):
		return errf(409, "conflict", "Der Eintrag wurde inzwischen geändert – bitte neu laden")
	case errors.Is(err, store.ErrUIDConflict):
		return errf(409, "conflict", "Ein Eintrag mit dieser UID existiert bereits")
	}
	return err
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

type collectionJSON struct {
	*store.Collection
	URL   string `json:"url"`
	Count *int   `json:"count,omitempty"`
}

func davCollectionURL(u *store.Account, c *store.Collection) string {
	home := "calendars"
	if c.Kind == store.KindAddressBook {
		home = "addressbooks"
	}
	return "/dav/" + home + "/" + url.PathEscape(u.Email) + "/" + url.PathEscape(c.Slug) + "/"
}

func (s *Server) collectionOut(ctx context.Context, u *store.Account, c *store.Collection) collectionJSON {
	out := collectionJSON{Collection: c, URL: davCollectionURL(u, c)}
	if c.Kind == store.KindAddressBook {
		n := s.Store.CountObjects(ctx, c)
		out.Count = &n
	}
	return out
}

func (s *Server) listCollections(kind string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		cols, err := s.Store.ListCollections(r.Context(), u.ID, kind)
		if err != nil {
			return nil, err
		}
		out := make([]collectionJSON, 0, len(cols))
		for _, c := range cols {
			out = append(out, s.collectionOut(r.Context(), u, c))
		}
		return out, nil
	}
}

func (s *Server) createCollection(kind string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		var req struct {
			Name        string   `json:"name"`
			Color       string   `json:"color"`
			Description string   `json:"description"`
			Components  []string `json:"components"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 200 {
			return nil, invalid("Name fehlt")
		}
		if req.Color != "" && !colorRe.MatchString(req.Color) {
			return nil, invalid("Ungültige Farbe")
		}
		c := &store.Collection{AccountID: u.ID, Kind: kind, Name: req.Name, Color: req.Color, Description: strings.TrimSpace(req.Description)}
		if kind == store.KindCalendar {
			for _, comp := range req.Components {
				comp = strings.ToUpper(strings.TrimSpace(comp))
				if comp != "VEVENT" && comp != "VTODO" {
					return nil, invalid("Unbekannter Komponententyp " + comp)
				}
				c.Components = append(c.Components, comp)
			}
		}
		if _, err := s.Store.ListCollections(r.Context(), u.ID, kind); err != nil { // creates the defaults first
			return nil, err
		}
		if err := s.Store.CreateCollection(r.Context(), c); err != nil {
			return nil, err
		}
		return s.collectionOut(r.Context(), u, c), nil
	}
}

func (s *Server) loadCollection(r *http.Request, u *store.Account, kind string) (*store.Collection, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	c, err := s.Store.GetCollection(r.Context(), u.ID, id)
	if err != nil {
		return nil, err
	}
	if c.Kind != kind {
		return nil, errNotFound
	}
	return c, nil
}

func (s *Server) patchCollection(kind string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		c, err := s.loadCollection(r, u, kind)
		if err != nil {
			return nil, err
		}
		var req struct {
			Name        *string `json:"name"`
			Color       *string `json:"color"`
			Description *string `json:"description"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name != nil {
			if n := strings.TrimSpace(*req.Name); n != "" && len(n) <= 200 {
				c.Name = n
			} else {
				return nil, invalid("Name fehlt")
			}
		}
		if req.Color != nil {
			if *req.Color != "" && !colorRe.MatchString(*req.Color) {
				return nil, invalid("Ungültige Farbe")
			}
			c.Color = *req.Color
		}
		if req.Description != nil {
			c.Description = strings.TrimSpace(*req.Description)
		}
		if err := s.Store.UpdateCollection(r.Context(), c); err != nil {
			return nil, err
		}
		return s.collectionOut(r.Context(), u, c), nil
	}
}

func (s *Server) deleteCollection(kind string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		c, err := s.loadCollection(r, u, kind)
		if err != nil {
			return nil, err
		}
		all, err := s.Store.ListCollections(r.Context(), u.ID, kind)
		if err != nil {
			return nil, err
		}
		if len(all) <= 1 {
			if kind == store.KindCalendar {
				return nil, errf(409, "conflict", "Der letzte Kalender kann nicht gelöscht werden")
			}
			return nil, errf(409, "conflict", "Das letzte Adressbuch kann nicht gelöscht werden")
		}
		return ok(), s.Store.DeleteCollection(r.Context(), u.ID, c.ID)
	}
}

// pickCollection returns the requested collection or the first one of the
// kind that accepts comp.
func (s *Server) pickCollection(ctx context.Context, u *store.Account, kind string, id *int64, comp string) (*store.Collection, error) {
	if id != nil && *id > 0 {
		c, err := s.Store.GetCollection(ctx, u.ID, *id)
		if err != nil || c.Kind != kind {
			return nil, invalid("Unbekannter Kalender bzw. unbekanntes Adressbuch")
		}
		if kind == store.KindCalendar && !c.Supports(comp) {
			return nil, invalid("Dieser Kalender unterstützt diesen Eintragstyp nicht")
		}
		return c, nil
	}
	cols, err := s.Store.ListCollections(ctx, u.ID, kind)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if kind == store.KindAddressBook || c.Supports(comp) {
			return c, nil
		}
	}
	return nil, invalid("Kein passender Kalender vorhanden")
}

// resourceName derives the DAV resource name for a new object.
func resourceName(uid, ext string) string {
	if store.ValidSlug(uid) && len(uid) <= 100 {
		return uid + ext
	}
	return pim.NewUID() + ext
}

// sourceMessage keeps a mail reference only if the mail belongs to the user.
func (s *Server) sourceMessage(ctx context.Context, u *store.Account, id *int64) *int64 {
	if id == nil {
		return nil
	}
	if _, err := s.Store.GetMessage(ctx, u.ID, *id); err != nil {
		return nil
	}
	return id
}

// saveObject writes data for an existing object, moving it when the target
// collection differs.
func (s *Server) saveObject(ctx context.Context, from, to *store.Collection, obj *store.DavObject, data []byte) (*store.DavObject, error) {
	if from.ID == to.ID {
		o, _, err := s.Store.PutObject(ctx, to, obj.Name, data, store.PutOptions{IfMatch: obj.ETag})
		return o, pimErr(err)
	}
	o, _, err := s.Store.PutObject(ctx, to, obj.Name, data, store.PutOptions{IfNoneMatch: true, SourceMessageID: obj.SourceMessageID})
	if err != nil {
		return nil, pimErr(err)
	}
	if err := s.Store.DeleteObject(ctx, from, obj.Name, ""); err != nil {
		return nil, err
	}
	return o, nil
}

// ---- events ----

func eventOut(obj *store.DavObject, cal *ical.Calendar, in pim.Instance) pim.Event {
	ev := pim.EventFromInstance(pim.Master(cal), in)
	ev.ID, ev.CalendarID, ev.SourceMessageID = obj.ID, obj.CollectionID, obj.SourceMessageID
	return ev
}

func singleEvent(obj *store.DavObject) (pim.Event, error) {
	cal, err := pim.ParseCalendar(obj.Data)
	if err != nil {
		return pim.Event{}, err
	}
	in, ok := pim.Single(cal)
	if !ok {
		return pim.Event{}, errors.New("event without start")
	}
	return eventOut(obj, cal, in), nil
}

func parseRangeParam(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	q := r.URL.Query()
	from, okF := parseRangeParam(q.Get("from"))
	to, okT := parseRangeParam(q.Get("to"))
	now := time.Now().UTC()
	if !okF {
		from = now.AddDate(-5, 0, 0)
	}
	if !okT {
		to = now.AddDate(5, 0, 0)
	}
	if !to.After(from) {
		return nil, invalid("Ungültiger Zeitraum")
	}
	// Floating times are indexed as UTC: widen the window by a day and let
	// the client place the events.
	from, to = from.Add(-24*time.Hour), to.Add(24*time.Hour)
	var coll int64
	if v := q.Get("calendar"); v != "" {
		coll, _ = strconv.ParseInt(v, 10, 64)
	}
	if _, err := s.Store.ListCollections(r.Context(), u.ID, store.KindCalendar); err != nil {
		return nil, err
	}
	f, t := from.Unix(), to.Unix()
	objs, err := s.Store.ListCalendarObjects(r.Context(), u.ID, "VEVENT", coll, &f, &t)
	if err != nil {
		return nil, err
	}
	out := []pim.Event{}
	for _, obj := range objs {
		cal, err := pim.ParseCalendar(obj.Data)
		if err != nil {
			continue
		}
		for _, in := range pim.Expand(cal, from, to) {
			out = append(out, eventOut(obj, cal, in))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var in pim.EventInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	coll, err := s.pickCollection(r.Context(), u, store.KindCalendar, in.CalendarID, "VEVENT")
	if err != nil {
		return nil, err
	}
	uid := pim.NewUID()
	cal, err := pim.NewEvent(uid, in)
	if err != nil {
		return nil, invalid(err.Error())
	}
	data, err := pim.EncodeCalendar(cal)
	if err != nil {
		return nil, err
	}
	obj, _, err := s.Store.PutObject(r.Context(), coll, resourceName(uid, ".ics"), data,
		store.PutOptions{IfNoneMatch: true, SourceMessageID: s.sourceMessage(r.Context(), u, in.SourceMessageID)})
	if err != nil {
		return nil, pimErr(err)
	}
	return singleEvent(obj)
}

func (s *Server) loadObject(r *http.Request, u *store.Account, kind, comp string) (*store.DavObject, *store.Collection, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, nil, err
	}
	obj, err := s.Store.GetObjectByID(r.Context(), u.ID, kind, id)
	if err != nil {
		return nil, nil, err
	}
	if comp != "" && obj.Component != comp {
		return nil, nil, errNotFound
	}
	coll, err := s.Store.GetCollection(r.Context(), u.ID, obj.CollectionID)
	if err != nil {
		return nil, nil, err
	}
	return obj, coll, nil
}

func (s *Server) updateEvent(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	obj, coll, err := s.loadObject(r, u, store.KindCalendar, "VEVENT")
	if err != nil {
		return nil, err
	}
	var in pim.EventInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	target := coll
	if in.CalendarID != nil && *in.CalendarID != coll.ID {
		if target, err = s.pickCollection(r.Context(), u, store.KindCalendar, in.CalendarID, "VEVENT"); err != nil {
			return nil, err
		}
	}
	cal, err := pim.ParseCalendar(obj.Data)
	if err != nil {
		return nil, invalid("Der gespeicherte Termin ist beschädigt")
	}
	if err := pim.ApplyEvent(pim.Master(cal), in, false); err != nil {
		return nil, invalid(err.Error())
	}
	saved, err := s.saveObject(r.Context(), coll, target, obj, pim.EncodeLenient(cal))
	if err != nil {
		return nil, err
	}
	return singleEvent(saved)
}

func (s *Server) deleteCalendarObject(comp string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		obj, coll, err := s.loadObject(r, u, store.KindCalendar, comp)
		if err != nil {
			return nil, err
		}
		return ok(), s.Store.DeleteObject(r.Context(), coll, obj.Name, "")
	}
}

// ---- tasks ----

func taskOut(obj *store.DavObject) (pim.Task, error) {
	cal, err := pim.ParseCalendar(obj.Data)
	if err != nil {
		return pim.Task{}, err
	}
	t := pim.TaskFrom(cal)
	t.ID, t.CalendarID, t.SourceMessageID, t.CreatedAt = obj.ID, obj.CollectionID, obj.SourceMessageID, obj.CreatedAt
	return t, nil
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var coll int64
	if v := r.URL.Query().Get("calendar"); v != "" {
		coll, _ = strconv.ParseInt(v, 10, 64)
	}
	if _, err := s.Store.ListCollections(r.Context(), u.ID, store.KindCalendar); err != nil {
		return nil, err
	}
	objs, err := s.Store.ListCalendarObjects(r.Context(), u.ID, "VTODO", coll, nil, nil)
	if err != nil {
		return nil, err
	}
	out := []pim.Task{}
	for _, obj := range objs {
		if t, err := taskOut(obj); err == nil {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Done != b.Done {
			return !a.Done
		}
		da, db := "9999", "9999"
		if a.Due != nil {
			da = *a.Due
		}
		if b.Due != nil {
			db = *b.Due
		}
		if da != db {
			return da < db
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	return out, nil
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var in pim.TaskInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		return nil, invalid("Titel fehlt")
	}
	coll, err := s.pickCollection(r.Context(), u, store.KindCalendar, in.CalendarID, "VTODO")
	if err != nil {
		return nil, err
	}
	if in.Done == nil {
		f := false
		in.Done = &f
	}
	uid := pim.NewUID()
	cal, err := pim.NewTask(uid, in)
	if err != nil {
		return nil, invalid(err.Error())
	}
	data, err := pim.EncodeCalendar(cal)
	if err != nil {
		return nil, err
	}
	obj, _, err := s.Store.PutObject(r.Context(), coll, resourceName(uid, ".ics"), data,
		store.PutOptions{IfNoneMatch: true, SourceMessageID: s.sourceMessage(r.Context(), u, in.SourceMessageID)})
	if err != nil {
		return nil, pimErr(err)
	}
	return taskOut(obj)
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	obj, coll, err := s.loadObject(r, u, store.KindCalendar, "VTODO")
	if err != nil {
		return nil, err
	}
	var in pim.TaskInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	target := coll
	if in.CalendarID != nil && *in.CalendarID != coll.ID {
		if target, err = s.pickCollection(r.Context(), u, store.KindCalendar, in.CalendarID, "VTODO"); err != nil {
			return nil, err
		}
	}
	cal, err := pim.ParseCalendar(obj.Data)
	if err != nil {
		return nil, invalid("Die gespeicherte Aufgabe ist beschädigt")
	}
	if err := pim.ApplyTask(pim.Master(cal), in); err != nil {
		return nil, invalid(err.Error())
	}
	saved, err := s.saveObject(r.Context(), coll, target, obj, pim.EncodeLenient(cal))
	if err != nil {
		return nil, err
	}
	return taskOut(saved)
}

// ---- ICS import / export ----

type importResult struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
}

const maxImportBytes = 8 << 20

func (s *Server) importCalendar(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	coll, err := s.loadCollection(r, u, store.KindCalendar)
	if err != nil {
		return nil, err
	}
	var req struct {
		Data string `json:"data"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if len(req.Data) > maxImportBytes {
		return nil, errf(413, "too_large", "Die Datei ist zu groß")
	}
	var res importResult
	// A file may contain several VCALENDAR blocks.
	for _, block := range splitBlocks(req.Data, "VCALENDAR") {
		cal, err := pim.ParseCalendar([]byte(block))
		if err != nil {
			res.Skipped++
			continue
		}
		for _, part := range pim.Split(cal, pim.NewUID) {
			m, err := pim.Validate(part)
			if err != nil || !coll.Supports(m.Component) {
				res.Skipped++
				continue
			}
			data := pim.EncodeLenient(part)
			name := resourceName(m.UID, ".ics")
			opts := store.PutOptions{}
			if old, err := s.Store.GetObjectByUID(r.Context(), coll, m.UID); err == nil {
				name = old.Name
			}
			if _, created, err := s.Store.PutObject(r.Context(), coll, name, data, opts); err != nil {
				res.Skipped++
			} else if created {
				res.Imported++
			} else {
				res.Updated++
			}
		}
	}
	if res.Imported+res.Updated == 0 && res.Skipped == 0 {
		return nil, invalid("Keine Termine oder Aufgaben in der Datei gefunden")
	}
	return res, nil
}

// splitBlocks returns the BEGIN:<name> ... END:<name> blocks of a text file.
func splitBlocks(data, name string) []string {
	var out []string
	upper := strings.ToUpper(data)
	begin, end := "BEGIN:"+name, "END:"+name
	for {
		i := strings.Index(upper, begin)
		if i < 0 {
			break
		}
		j := strings.Index(upper[i:], end)
		if j < 0 {
			break
		}
		j += i + len(end)
		out = append(out, data[i:j]+"\r\n")
		data, upper = data[j:], upper[j:]
	}
	return out
}

var fileNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func downloadName(name, ext string) string {
	n := strings.Trim(fileNameRe.ReplaceAllString(name, "-"), "-.")
	if n == "" {
		n = "export"
	}
	return n + ext
}

func (s *Server) exportCalendar(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	coll, err := s.loadCollection(r, u, store.KindCalendar)
	if err != nil {
		return nil, err
	}
	objs, err := s.Store.ListObjects(r.Context(), coll)
	if err != nil {
		return nil, err
	}
	datas := make([][]byte, len(objs))
	for i, o := range objs {
		datas[i] = o.Data
	}
	out, err := pim.Merge(coll.Name, datas)
	if err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName(coll.Name, ".ics")))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
	return nil, nil
}

// ---- contacts ----

func contactOut(obj *store.DavObject) (pim.Contact, error) {
	card, err := pim.ParseCard(obj.Data)
	if err != nil {
		return pim.Contact{}, err
	}
	c := pim.ContactFrom(card)
	c.ID, c.AddressBookID, c.UpdatedAt = obj.ID, obj.CollectionID, obj.UpdatedAt
	return c, nil
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	q := r.URL.Query()
	var book int64
	if v := q.Get("addressBook"); v != "" {
		book, _ = strconv.ParseInt(v, 10, 64)
	}
	if _, err := s.Store.ListCollections(r.Context(), u.ID, store.KindAddressBook); err != nil {
		return nil, err
	}
	objs, err := s.Store.ListAddressObjects(r.Context(), u.ID, book, q.Get("q"), queryInt(r, "limit", 0, 5000))
	if err != nil {
		return nil, err
	}
	out := []pim.Contact{}
	for _, obj := range objs {
		if c, err := contactOut(obj); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Server) getContact(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	obj, _, err := s.loadObject(r, u, store.KindAddressBook, "")
	if err != nil {
		return nil, err
	}
	return contactOut(obj)
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var in pim.ContactInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	book, err := s.pickCollection(r.Context(), u, store.KindAddressBook, in.AddressBookID, "")
	if err != nil {
		return nil, err
	}
	uid := pim.NewUID()
	card, err := pim.NewCard(uid, in)
	if err != nil {
		return nil, invalid(err.Error())
	}
	data, err := pim.EncodeCard(card)
	if err != nil {
		return nil, err
	}
	obj, _, err := s.Store.PutObject(r.Context(), book, resourceName(uid, ".vcf"), data, store.PutOptions{IfNoneMatch: true})
	if err != nil {
		return nil, pimErr(err)
	}
	return contactOut(obj)
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	obj, book, err := s.loadObject(r, u, store.KindAddressBook, "")
	if err != nil {
		return nil, err
	}
	var in pim.ContactInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	target := book
	if in.AddressBookID != nil && *in.AddressBookID != book.ID {
		if target, err = s.pickCollection(r.Context(), u, store.KindAddressBook, in.AddressBookID, ""); err != nil {
			return nil, err
		}
	}
	card, err := pim.ParseCard(obj.Data)
	if err != nil {
		return nil, invalid("Der gespeicherte Kontakt ist beschädigt")
	}
	if err := pim.ApplyContact(card, in); err != nil {
		return nil, invalid(err.Error())
	}
	data, err := pim.EncodeCard(card)
	if err != nil {
		return nil, err
	}
	saved, err := s.saveObject(r.Context(), book, target, obj, data)
	if err != nil {
		return nil, err
	}
	return contactOut(saved)
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	obj, book, err := s.loadObject(r, u, store.KindAddressBook, "")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteObject(r.Context(), book, obj.Name, "")
}

func (s *Server) importContacts(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		AddressBookID *int64 `json:"addressBookId"`
		Data          string `json:"data"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if len(req.Data) > maxImportBytes {
		return nil, errf(413, "too_large", "Die Datei ist zu groß")
	}
	book, err := s.pickCollection(r.Context(), u, store.KindAddressBook, req.AddressBookID, "")
	if err != nil {
		return nil, err
	}
	var res importResult
	for _, block := range splitBlocks(req.Data, "VCARD") {
		card, err := pim.ParseCard([]byte(block))
		if err != nil {
			res.Skipped++
			continue
		}
		uid := pim.EnsureUID(card, pim.NewUID)
		data, err := pim.EncodeCard(card)
		if err != nil {
			res.Skipped++
			continue
		}
		name := resourceName(uid, ".vcf")
		if old, err := s.Store.GetObjectByUID(r.Context(), book, uid); err == nil {
			name = old.Name
		}
		if _, created, err := s.Store.PutObject(r.Context(), book, name, data, store.PutOptions{}); err != nil {
			res.Skipped++
		} else if created {
			res.Imported++
		} else {
			res.Updated++
		}
	}
	if res.Imported+res.Updated+res.Skipped == 0 {
		return nil, invalid("Keine Kontakte in der Datei gefunden")
	}
	return res, nil
}

func (s *Server) exportContacts(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var book int64
	name := "kontakte"
	if v := r.URL.Query().Get("addressBook"); v != "" {
		book, _ = strconv.ParseInt(v, 10, 64)
		c, err := s.Store.GetCollection(r.Context(), u.ID, book)
		if err != nil || c.Kind != store.KindAddressBook {
			return nil, errNotFound
		}
		name = c.Name
	}
	objs, err := s.Store.ListAddressObjects(r.Context(), u.ID, book, "", 0)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, o := range objs {
		buf.Write(bytes.TrimRight(o.Data, "\r\n"))
		buf.WriteString("\r\n")
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName(name, ".vcf")))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
	return nil, nil
}

type suggestion struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Source string `json:"source"`
}

// suggestContacts feeds the address autocomplete: contacts first, then
// recently used recipients.
func (s *Server) suggestContacts(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit := queryInt(r, "limit", 10, 50)
	if limit == 0 {
		limit = 10
	}
	out := []suggestion{}
	seen := map[string]bool{}
	match := func(name, email string) bool {
		return q == "" || strings.Contains(strings.ToLower(name), q) || strings.Contains(email, q)
	}
	objs, err := s.Store.ListAddressObjects(r.Context(), u.ID, 0, q, 200)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		card, err := pim.ParseCard(o.Data)
		if err != nil {
			continue
		}
		info := pim.CardInfo(card)
		for _, e := range info.Emails {
			if seen[e] || !match(info.FN, e) {
				continue
			}
			seen[e] = true
			out = append(out, suggestion{Name: info.FN, Email: e, Source: "contact"})
		}
	}
	if len(out) < limit {
		recent, err := s.Store.RecentRecipients(r.Context(), u.ID, 300)
		if err != nil {
			return nil, err
		}
		for _, a := range recent {
			if seen[a.Email] || !match(a.Name, a.Email) {
				continue
			}
			seen[a.Email] = true
			out = append(out, suggestion{Name: a.Name, Email: a.Email, Source: "recent"})
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
