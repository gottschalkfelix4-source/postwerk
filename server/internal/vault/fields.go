package vault

import (
	"reflect"
)

// Struct fields of type string tagged `secret:"true"` are encrypted by
// EncryptFields and decrypted by DecryptFields. The walk follows nested
// structs (named and anonymous), pointers, slices, arrays and map values, so
// new settings sections only need the tag on their secret fields.

// EncryptFields encrypts all tagged fields of *v in place. It returns the
// number of fields that changed (plaintext that was encrypted).
func (k *Key) EncryptFields(v any) int {
	n := 0
	walkSecrets(reflect.ValueOf(v), func(f reflect.Value) error {
		s := f.String()
		if s != "" && !IsEncrypted(s) {
			f.SetString(k.Encrypt(s))
			n++
		}
		return nil
	})
	return n
}

// DecryptFields decrypts all tagged fields of *v in place. plain counts the
// tagged non-empty fields that were still plaintext (legacy values).
func (k *Key) DecryptFields(v any) (plain int, err error) {
	err = walkSecrets(reflect.ValueOf(v), func(f reflect.Value) error {
		s := f.String()
		if s == "" {
			return nil
		}
		if !IsEncrypted(s) {
			plain++
			return nil
		}
		out, err := k.Decrypt(s)
		if err != nil {
			return err
		}
		f.SetString(out)
		return nil
	})
	return plain, err
}

// SecretPaths lists the tagged fields of a struct type as dotted JSON-ish
// Go field paths (for documentation and tests).
func SecretPaths(v any) []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Type.Kind() == reflect.String && f.Tag.Get("secret") == "true" {
				out = append(out, prefix+f.Name)
				continue
			}
			walk(f.Type, prefix+f.Name+".")
		}
	}
	walk(reflect.TypeOf(v), "")
	return out
}

func walkSecrets(v reflect.Value, fn func(reflect.Value) error) error {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return walkSecrets(v.Elem(), fn)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			f := v.Field(i)
			if sf.Type.Kind() == reflect.String && sf.Tag.Get("secret") == "true" {
				if f.CanSet() {
					if err := fn(f); err != nil {
						return err
					}
				}
				continue
			}
			if err := walkSecrets(f, fn); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkSecrets(v.Index(i), fn); err != nil {
				return err
			}
		}
	case reflect.Map:
		// Map values are not addressable: copy, modify, store back.
		if v.IsNil() || !hasSecrets(v.Type().Elem()) {
			return nil
		}
		iter := v.MapRange()
		for iter.Next() {
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(iter.Value())
			if err := walkSecrets(cp, fn); err != nil {
				return err
			}
			v.SetMapIndex(iter.Key(), cp)
		}
	}
	return nil
}

func hasSecrets(t reflect.Type) bool {
	return len(SecretPaths(reflect.New(t).Interface())) > 0
}
