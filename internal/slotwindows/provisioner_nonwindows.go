//go:build !windows

package slotwindows

func New(options Options) (Provisioner, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
