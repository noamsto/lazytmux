package render

import "golang.org/x/term"

func MakeRaw(fd int) (func() error, error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, old) }, nil
}

func Size(fd int) (int, int, error) {
	return term.GetSize(fd)
}
