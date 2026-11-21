package export

import (
	"context"
	"io"
	"strings"

	"postwerk/internal/pim"
	"postwerk/internal/store"
)

// writeCollections exports every calendar as kalender/<slug>.ics and every
// address book as kontakte/<slug>.vcf. Empty collections are skipped.
func writeCollections(ctx context.Context, st *store.Store, accountID int64, add func(string) (io.Writer, error)) error {
	for _, kind := range []string{store.KindCalendar, store.KindAddressBook} {
		cols, err := st.ListCollections(ctx, accountID, kind)
		if err != nil {
			return err
		}
		for _, c := range cols {
			objs, err := st.ListObjects(ctx, c)
			if err != nil {
				return err
			}
			if len(objs) == 0 {
				continue
			}
			name := safeName(c.Slug)
			if kind == store.KindCalendar {
				data := make([][]byte, 0, len(objs))
				for _, o := range objs {
					data = append(data, o.Data)
				}
				merged, err := pim.Merge(c.Name, data)
				if err != nil {
					return err
				}
				w, err := add("kalender/" + name + ".ics")
				if err != nil {
					return err
				}
				if _, err := w.Write(merged); err != nil {
					return err
				}
				continue
			}
			w, err := add("kontakte/" + name + ".vcf")
			if err != nil {
				return err
			}
			for _, o := range objs {
				card := strings.TrimRight(string(o.Data), "\r\n") + "\r\n"
				if _, err := io.WriteString(w, card); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
