package pim

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
)

// ErrInvalidCard marks data that is not a valid vCard.
var ErrInvalidCard = errors.New("invalid vCard")

// ParseCards decodes all vCards in data (a .vcf file may hold many).
func ParseCards(data []byte) ([]vcard.Card, error) {
	dec := vcard.NewDecoder(bytes.NewReader(data))
	var out []vcard.Card
	for {
		c, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrInvalidCard, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// ParseCard decodes exactly one vCard.
func ParseCard(data []byte) (vcard.Card, error) {
	cards, err := ParseCards(data)
	if err != nil {
		return nil, err
	}
	if len(cards) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one vCard, got %d", ErrInvalidCard, len(cards))
	}
	return cards[0], nil
}

// EncodeCard serializes a card.
func EncodeCard(c vcard.Card) ([]byte, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CardMeta is the information extracted from a vCard for indexing.
type CardMeta struct {
	UID    string
	FN     string
	Emails []string
	Search string
}

// CardInfo returns the index data of a card.
func CardInfo(c vcard.Card) CardMeta {
	m := CardMeta{UID: strings.TrimSpace(c.Value(vcard.FieldUID)), FN: displayName(c)}
	parts := []string{m.FN, c.Value(vcard.FieldOrganization), c.Value(vcard.FieldNickname)}
	for _, f := range c[vcard.FieldEmail] {
		if e := strings.ToLower(strings.TrimSpace(f.Value)); e != "" {
			m.Emails = append(m.Emails, e)
			parts = append(parts, e)
		}
	}
	for _, f := range c[vcard.FieldTelephone] {
		parts = append(parts, f.Value, digits(f.Value))
	}
	m.Search = strings.ToLower(strings.Join(parts, " "))
	return m
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func displayName(c vcard.Card) string {
	if fn := strings.TrimSpace(c.PreferredValue(vcard.FieldFormattedName)); fn != "" {
		return fn
	}
	if n := c.Name(); n != nil {
		if s := strings.TrimSpace(strings.Join(nonEmpty(n.HonorificPrefix, n.GivenName, n.AdditionalName, n.FamilyName), " ")); s != "" {
			return s
		}
	}
	if org := strings.TrimSpace(strings.ReplaceAll(c.Value(vcard.FieldOrganization), ";", " ")); org != "" {
		return org
	}
	return strings.TrimSpace(c.PreferredValue(vcard.FieldEmail))
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Entry is a typed e-mail address or phone number.
type Entry struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Contact is the JSON shape of a vCard.
type Contact struct {
	ID            int64     `json:"id"`
	AddressBookID int64     `json:"addressBookId"`
	UID           string    `json:"uid"`
	Name          string    `json:"name"`
	FirstName     string    `json:"firstName"`
	LastName      string    `json:"lastName"`
	Emails        []Entry   `json:"emails"`
	Phones        []Entry   `json:"phones"`
	Organization  string    `json:"organization"`
	Title         string    `json:"title"`
	Notes         string    `json:"notes"`
	Birthday      *string   `json:"birthday"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ContactInput is the JSON body for creating or replacing a contact.
type ContactInput struct {
	AddressBookID *int64  `json:"addressBookId"`
	Name          string  `json:"name"`
	FirstName     string  `json:"firstName"`
	LastName      string  `json:"lastName"`
	Emails        []Entry `json:"emails"`
	Phones        []Entry `json:"phones"`
	Organization  string  `json:"organization"`
	Title         string  `json:"title"`
	Notes         string  `json:"notes"`
	Birthday      *string `json:"birthday"`
}

func entryType(f *vcard.Field, kinds []string, def string) string {
	for _, t := range f.Params.Types() {
		t = strings.ToLower(t)
		for _, k := range kinds {
			if t == k {
				return k
			}
		}
		if t == "mobile" || t == "iphone" {
			for _, k := range kinds {
				if k == "cell" {
					return k
				}
			}
		}
	}
	return def
}

var (
	emailKinds = []string{"home", "work"}
	phoneKinds = []string{"cell", "home", "work"}
)

// ContactFrom renders a card.
func ContactFrom(c vcard.Card) Contact {
	ct := Contact{Name: displayName(c), Emails: []Entry{}, Phones: []Entry{}}
	ct.UID = c.Value(vcard.FieldUID)
	if n := c.Name(); n != nil {
		ct.FirstName, ct.LastName = n.GivenName, n.FamilyName
	}
	for _, f := range c[vcard.FieldEmail] {
		if v := strings.TrimSpace(f.Value); v != "" {
			ct.Emails = append(ct.Emails, Entry{Type: entryType(f, emailKinds, "other"), Value: v})
		}
	}
	for _, f := range c[vcard.FieldTelephone] {
		v := strings.TrimSpace(strings.TrimPrefix(f.Value, "tel:"))
		if v != "" {
			ct.Phones = append(ct.Phones, Entry{Type: entryType(f, phoneKinds, "other"), Value: v})
		}
	}
	ct.Organization = strings.TrimRight(strings.ReplaceAll(c.Value(vcard.FieldOrganization), ";", ", "), ", ")
	ct.Title = c.Value(vcard.FieldTitle)
	ct.Notes = c.Value(vcard.FieldNote)
	if b := parseBirthday(c.Value(vcard.FieldBirthday)); b != "" {
		ct.Birthday = &b
	}
	return ct
}

func parseBirthday(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 10 {
		if t, err := time.Parse("2006-01-02", v[:10]); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if len(v) >= 8 {
		if t, err := time.Parse("20060102", v[:8]); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// NewCard builds a vCard 3.0 (the version iOS and macOS handle best).
func NewCard(uid string, in ContactInput) (vcard.Card, error) {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "3.0")
	c.SetValue(vcard.FieldUID, uid)
	if err := ApplyContact(c, in); err != nil {
		return nil, err
	}
	return c, nil
}

func normEntries(in []Entry) []Entry {
	var out []Entry
	for _, e := range in {
		if v := strings.TrimSpace(e.Value); v != "" {
			out = append(out, Entry{Type: strings.ToLower(strings.TrimSpace(e.Type)), Value: v})
		}
	}
	return out
}

// ApplyContact writes the input into a card. Fields the web interface does
// not edit (photo, addresses, ...) stay untouched; e-mail addresses and phone
// numbers keep their original parameters where the value is unchanged.
func ApplyContact(c vcard.Card, in ContactInput) error {
	first, last := strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimSpace(first + " " + last)
	}
	emails, phones := normEntries(in.Emails), normEntries(in.Phones)
	org := strings.TrimSpace(in.Organization)
	if name == "" {
		name = org
	}
	if name == "" && len(emails) > 0 {
		name = emails[0].Value
	}
	if name == "" {
		return errors.New("Name, Organisation oder E-Mail-Adresse angeben")
	}
	for _, e := range emails {
		if !strings.Contains(e.Value, "@") {
			return fmt.Errorf("Ungültige E-Mail-Adresse: %s", e.Value)
		}
	}
	c.SetValue(vcard.FieldFormattedName, name)
	n := c.Name()
	if n == nil {
		n = &vcard.Name{}
	}
	if first == "" && last == "" && n.GivenName == "" && n.FamilyName == "" {
		// Derive N from the display name ("Erika Mustermann").
		if i := strings.LastIndex(name, " "); i > 0 && name != org && !strings.Contains(name, "@") {
			first, last = name[:i], name[i+1:]
		}
	}
	n.GivenName, n.FamilyName = first, last
	c.SetName(n)

	c[vcard.FieldEmail] = mergeEntries(c[vcard.FieldEmail], emails, emailKinds, true)
	c[vcard.FieldTelephone] = mergeEntries(c[vcard.FieldTelephone], phones, phoneKinds, false)
	setOrDel(c, vcard.FieldOrganization, org)
	setOrDel(c, vcard.FieldTitle, strings.TrimSpace(in.Title))
	setOrDel(c, vcard.FieldNote, strings.TrimSpace(in.Notes))
	if in.Birthday != nil && strings.TrimSpace(*in.Birthday) != "" {
		b := strings.TrimSpace(*in.Birthday)
		if parseBirthday(b) == "" {
			return errors.New("Ungültiges Geburtsdatum")
		}
		if parseBirthday(c.Value(vcard.FieldBirthday)) != parseBirthday(b) {
			c.SetValue(vcard.FieldBirthday, parseBirthday(b))
		}
	} else {
		delete(c, vcard.FieldBirthday)
	}
	c.SetRevision(time.Now().UTC())
	return nil
}

func setOrDel(c vcard.Card, k, v string) {
	if v == "" {
		delete(c, k)
		return
	}
	if k == vcard.FieldOrganization && c.Value(k) != "" &&
		strings.TrimRight(strings.ReplaceAll(c.Value(k), ";", ", "), ", ") == v {
		return // unchanged; keep the structured value
	}
	c.SetValue(k, v)
}

// mergeEntries rebuilds a multi-valued field from the input while keeping
// the parameters of unchanged values.
func mergeEntries(old []*vcard.Field, in []Entry, kinds []string, email bool) []*vcard.Field {
	byValue := map[string]*vcard.Field{}
	def := "other"
	for _, f := range old {
		byValue[strings.ToLower(strings.TrimSpace(f.Value))] = f
	}
	var out []*vcard.Field
	for _, e := range in {
		if f, ok := byValue[strings.ToLower(e.Value)]; ok && entryType(f, kinds, def) == e.Type {
			out = append(out, f)
			continue
		}
		f := &vcard.Field{Value: e.Value, Params: vcard.Params{}}
		var types []string
		if email {
			types = append(types, "INTERNET")
		}
		switch e.Type {
		case "home", "work", "cell":
			types = append(types, strings.ToUpper(e.Type))
		case "other":
			if !email {
				types = append(types, "VOICE")
			}
		}
		if len(types) > 0 {
			f.Params[vcard.ParamType] = types
		}
		out = append(out, f)
	}
	return out
}

// EnsureUID gives a card a UID and VERSION if it lacks them.
func EnsureUID(c vcard.Card, newUID func() string) string {
	uid := strings.TrimSpace(c.Value(vcard.FieldUID))
	if uid == "" {
		uid = newUID()
		c.SetValue(vcard.FieldUID, uid)
	}
	if c.Value(vcard.FieldVersion) == "" {
		c.SetValue(vcard.FieldVersion, "3.0")
	}
	return uid
}

// NewUID returns a random UUID (version 4) for new objects.
func NewUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
