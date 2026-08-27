package sealed

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	KeyBytes  = 32
	FrameSize = 64 * 1024

	saltBytes  = 16
	tagBytes   = 16
	nonceBytes = 12
	version    = 1
	info       = "marstack backup v1"
)

var magic = [4]byte{'M', 'S', 'B', 'K'}

var (
	ErrCorrupt   = errors.New("the sealed stream does not decrypt")
	ErrTruncated = errors.New("the sealed stream ends before its final frame")
)

type Key struct {
	secret [KeyBytes]byte
}

func ParseKey(text string) (Key, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return Key{}, fmt.Errorf("the key is not hex: %w", err)
	}
	if len(raw) != KeyBytes {
		return Key{}, fmt.Errorf("the key is %d bytes, want %d as %d hex characters",
			len(raw), KeyBytes, KeyBytes*2)
	}

	var k Key
	copy(k.secret[:], raw)
	return k, nil
}

func NewKey() (Key, string) {
	var k Key
	_, _ = rand.Read(k.secret[:])
	return k, hex.EncodeToString(k.secret[:])
}

func (k Key) ID() string {
	sum := sha256.Sum256(k.secret[:])
	return hex.EncodeToString(sum[:8])
}

func (k Key) frameKey(salt []byte) (cipher.AEAD, error) {
	derived, err := hkdf.Key(sha256.New, k.secret[:], salt, info, KeyBytes)
	if err != nil {
		return nil, fmt.Errorf("derive the frame key: %w", err)
	}

	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("build the cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func nonceFor(frame uint64) []byte {
	nonce := make([]byte, nonceBytes)
	binary.BigEndian.PutUint64(nonce[nonceBytes-8:], frame)
	return nonce
}

func Seal(dst io.Writer, src io.Reader, k Key) (int64, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return 0, fmt.Errorf("draw a salt: %w", err)
	}

	aead, err := k.frameKey(salt)
	if err != nil {
		return 0, err
	}

	header := make([]byte, 0, len(magic)+1+saltBytes)
	header = append(header, magic[:]...)
	header = append(header, version)
	header = append(header, salt...)
	if _, err := dst.Write(header); err != nil {
		return 0, fmt.Errorf("write the header: %w", err)
	}

	var (
		plain   = make([]byte, FrameSize)
		sealBuf = make([]byte, 0, FrameSize+tagBytes)
		frame   uint64
		total   int64
	)

	for {
		read, readErr := io.ReadFull(src, plain)
		last := readErr != nil

		if read > 0 || last {
			flag := byte(0)
			if last {
				flag = 1
			}

			sealBuf = aead.Seal(sealBuf[:0], nonceFor(frame), plain[:read], []byte{flag})
			if _, err := dst.Write(sealBuf); err != nil {
				return total, fmt.Errorf("write a frame: %w", err)
			}
			total += int64(read)
			frame++
		}

		if last {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				return total, nil
			}
			return total, fmt.Errorf("read the plaintext: %w", readErr)
		}
	}
}

type opener struct {
	src    io.Reader
	aead   cipher.AEAD
	frame  uint64
	buf    []byte
	plain  []byte
	unread []byte
	done   bool
}

func Open(src io.Reader, k Key) (io.Reader, error) {
	header := make([]byte, len(magic)+1+saltBytes)
	if _, err := io.ReadFull(src, header); err != nil {
		return nil, ErrTruncated
	}
	if string(header[:len(magic)]) != string(magic[:]) {
		return nil, ErrCorrupt
	}
	if header[len(magic)] != version {
		return nil, fmt.Errorf("the sealed stream is version %d, this build reads %d",
			header[len(magic)], version)
	}

	aead, err := k.frameKey(header[len(magic)+1:])
	if err != nil {
		return nil, err
	}

	return &opener{
		src:   src,
		aead:  aead,
		buf:   make([]byte, FrameSize+tagBytes),
		plain: make([]byte, 0, FrameSize),
	}, nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.unread) == 0 {
		if o.done {
			return 0, io.EOF
		}
		if err := o.next(); err != nil {
			return 0, err
		}
	}

	taken := copy(p, o.unread)
	o.unread = o.unread[taken:]
	return taken, nil
}

func (o *opener) next() error {
	read, err := io.ReadFull(o.src, o.buf)
	switch {
	case err == nil:
	case errors.Is(err, io.ErrUnexpectedEOF):
	case errors.Is(err, io.EOF):
		return ErrTruncated
	default:
		return fmt.Errorf("read a frame: %w", err)
	}
	if read < tagBytes {
		return ErrTruncated
	}

	short := read < len(o.buf)
	if err := o.unseal(read, short); err != nil {
		if short {
			return err
		}
		return o.unseal(read, true)
	}
	return nil
}

func (o *opener) unseal(read int, last bool) error {
	flag := byte(0)
	if last {
		flag = 1
	}

	opened, err := o.aead.Open(o.plain[:0], nonceFor(o.frame), o.buf[:read], []byte{flag})
	if err != nil {
		return ErrCorrupt
	}

	o.frame++
	o.plain = opened
	o.unread = opened
	o.done = last
	return nil
}
