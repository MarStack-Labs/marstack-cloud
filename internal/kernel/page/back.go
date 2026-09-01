package page

import (
	"errors"
	"strconv"
)

func Back(after Cursor) (int64, error) {
	if after.Empty() {
		return 0, nil
	}

	id, err := strconv.ParseInt(after.ID, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("after is not a cursor this platform handed out")
	}
	return id, nil
}

func Backward(id int64) string {
	return Encode("", strconv.FormatInt(id, 10))
}
