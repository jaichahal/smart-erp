package approvals

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID returns a 26-character Crockford ULID. Event ids must match this shape.
func NewULID(at time.Time) (string, error) {
	var id [16]byte
	ms := uint64(at.UTC().UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	if _, err := rand.Read(id[6:]); err != nil {
		return "", err
	}
	return encodeULID(id), nil
}

func encodeULID(id [16]byte) string {
	dst := [26]byte{
		crockford[(id[0]&224)>>5],
		crockford[id[0]&31],
		crockford[(id[1]&248)>>3],
		crockford[((id[1]&7)<<2)|((id[2]&192)>>6)],
		crockford[(id[2]&62)>>1],
		crockford[((id[2]&1)<<4)|((id[3]&240)>>4)],
		crockford[((id[3]&15)<<1)|((id[4]&128)>>7)],
		crockford[(id[4]&124)>>2],
		crockford[((id[4]&3)<<3)|((id[5]&224)>>5)],
		crockford[id[5]&31],
		crockford[(id[6]&248)>>3],
		crockford[((id[6]&7)<<2)|((id[7]&192)>>6)],
		crockford[(id[7]&62)>>1],
		crockford[((id[7]&1)<<4)|((id[8]&240)>>4)],
		crockford[((id[8]&15)<<1)|((id[9]&128)>>7)],
		crockford[(id[9]&124)>>2],
		crockford[((id[9]&3)<<3)|((id[10]&224)>>5)],
		crockford[id[10]&31],
		crockford[(id[11]&248)>>3],
		crockford[((id[11]&7)<<2)|((id[12]&192)>>6)],
		crockford[(id[12]&62)>>1],
		crockford[((id[12]&1)<<4)|((id[13]&240)>>4)],
		crockford[((id[13]&15)<<1)|((id[14]&128)>>7)],
		crockford[(id[14]&124)>>2],
		crockford[((id[14]&3)<<3)|((id[15]&224)>>5)],
		crockford[id[15]&31],
	}
	return string(dst[:])
}
