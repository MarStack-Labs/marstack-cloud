package page

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const separator = "\x00"

type Window struct {
	Limit int
	After Cursor
}

type Cursor struct {
	Order string
	ID    string
}

func (c Cursor) Empty() bool {
	return c.Order == "" && c.ID == ""
}

func From(r *http.Request, fallback, ceiling int) (Window, error) {
	window := Window{Limit: fallback}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return Window{}, errors.New("limit must be a positive whole number")
		}
		window.Limit = limit
	}
	if window.Limit > ceiling {
		window.Limit = ceiling
	}

	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err := Decode(raw)
		if err != nil {
			return Window{}, err
		}
		window.After = after
	}
	return window, nil
}

func Encode(order, id string) string {
	if order == "" && id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(order + separator + id))
}

func Decode(raw string) (Cursor, error) {
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, errors.New("after is not a cursor this platform handed out")
	}

	order, id, found := strings.Cut(string(blob), separator)
	if !found {
		return Cursor{}, errors.New("after is not a cursor this platform handed out")
	}
	return Cursor{Order: order, ID: id}, nil
}
