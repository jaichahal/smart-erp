package journeys

import (
	"crypto/rand"
	"time"
)

// crockford is the ULID alphabet (excludes I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID returns a 26-character ULID. The timestamp comes from the database
// clock the caller already read, so the id does not depend on the process clock
// beyond the random component.
func newULID(at time.Time) (string, error) {
	ms := uint64(at.UnixMilli())
	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	return encodeULID(raw), nil
}

// encodeULID is the standard 128-bit to 26-character Crockford base32 mapping.
func encodeULID(u [16]byte) string {
	var dst [26]byte
	dst[0] = crockford[(u[0]&224)>>5]
	dst[1] = crockford[u[0]&31]
	dst[2] = crockford[(u[1]&248)>>3]
	dst[3] = crockford[((u[1]&7)<<2)|((u[2]&192)>>6)]
	dst[4] = crockford[(u[2]&62)>>1]
	dst[5] = crockford[((u[2]&1)<<4)|((u[3]&240)>>4)]
	dst[6] = crockford[((u[3]&15)<<1)|((u[4]&128)>>7)]
	dst[7] = crockford[(u[4]&124)>>2]
	dst[8] = crockford[((u[4]&3)<<3)|((u[5]&224)>>5)]
	dst[9] = crockford[u[5]&31]
	dst[10] = crockford[(u[6]&248)>>3]
	dst[11] = crockford[((u[6]&7)<<2)|((u[7]&192)>>6)]
	dst[12] = crockford[(u[7]&62)>>1]
	dst[13] = crockford[((u[7]&1)<<4)|((u[8]&240)>>4)]
	dst[14] = crockford[((u[8]&15)<<1)|((u[9]&128)>>7)]
	dst[15] = crockford[(u[9]&124)>>2]
	dst[16] = crockford[((u[9]&3)<<3)|((u[10]&224)>>5)]
	dst[17] = crockford[u[10]&31]
	dst[18] = crockford[(u[11]&248)>>3]
	dst[19] = crockford[((u[11]&7)<<2)|((u[12]&192)>>6)]
	dst[20] = crockford[(u[12]&62)>>1]
	dst[21] = crockford[((u[12]&1)<<4)|((u[13]&240)>>4)]
	dst[22] = crockford[((u[13]&15)<<1)|((u[14]&128)>>7)]
	dst[23] = crockford[(u[14]&124)>>2]
	dst[24] = crockford[((u[14]&3)<<3)|((u[15]&224)>>5)]
	dst[25] = crockford[u[15]&31]
	return string(dst[:])
}
