package backend

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

func WaitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func NetworkError(err error) bool {
	var e net.Error
	return errors.As(err, &e) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func FirstError(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
