package providers

import (
	"fmt"

	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/source/huggingface"
	"github.com/chieworks/mstore/internal/source/modelscope"
)

type ScanError struct {
	Provider string
	Err      error
}

func (e ScanError) Error() string { return fmt.Sprintf("%s: %v", e.Provider, e.Err) }
func (e ScanError) Unwrap() error { return e.Err }

func Scan(provider string) ([]source.Model, []error) {
	var models []source.Model
	var errs []error
	if provider == "all" || provider == "hf" {
		root, err := huggingface.CacheRoot()
		if err == nil {
			var got []source.Model
			got, err = huggingface.Scan(root)
			models = append(models, got...)
		}
		if err != nil {
			errs = append(errs, ScanError{Provider: "hf", Err: err})
		}
	}
	if provider == "all" || provider == "ms" {
		root, err := modelscope.CacheRoot()
		if err == nil {
			var got []source.Model
			got, err = modelscope.Scan(root)
			models = append(models, got...)
		}
		if err != nil {
			errs = append(errs, ScanError{Provider: "ms", Err: err})
		}
	}
	return models, errs
}

func Resolve(r source.Ref) (source.Model, error) {
	switch r.Provider {
	case "hf":
		return huggingface.Resolve(r)
	case "ms":
		return modelscope.Resolve(r)
	default:
		return source.Model{}, fmt.Errorf("unsupported provider %q", r.Provider)
	}
}
