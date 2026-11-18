package app

import (
	"reflect"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

// normalizeBoundPaths runs after either wire decoder, before handlers use paths
// or client hashes. Only vault path fields are visited; server filesystem paths
// (e.g. database Path and file SavePath) are outside this contract.
func normalizeBoundPaths(obj any) *ValidError {
	return normalizePathValue(reflect.ValueOf(obj))
}

func normalizePathValue(v reflect.Value) *ValidError {
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return normalizePathValue(v.Elem())
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := normalizePathValue(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for _, name := range []string{"Path", "OldPath", "NewPath", "OriginalPath"} {
			p := v.FieldByName(name)
			if !p.IsValid() || p.Kind() != reflect.String || !p.CanSet() || !strings.Contains(p.String(), `\`) {
				continue
			}
			// Admin database configuration also has a Path field, but no vault/hash.
			if name == "Path" && !v.FieldByName("PathHash").IsValid() && !v.FieldByName("Vault").IsValid() {
				continue
			}
			cleaned, ok := util.NormalizeBackslashPath(p.String())
			if !ok {
				return &ValidError{Key: name, Message: "invalid vault-relative path"}
			}
			p.SetString(cleaned)
			hash := v.FieldByName(name + "Hash")
			if hash.IsValid() && hash.Kind() == reflect.String && hash.CanSet() {
				hash.SetString(util.EncodeHash32(cleaned))
			}
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue
			}
			f := v.Field(i)
			if f.Kind() == reflect.Slice || f.Kind() == reflect.Array || f.Kind() == reflect.Struct || f.Kind() == reflect.Pointer {
				if err := normalizePathValue(f); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
