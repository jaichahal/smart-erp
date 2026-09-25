package sales

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newID(at time.Time) string {
	ms := uint64(at.UnixMilli())
	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	_, _ = rand.Read(raw[6:])
	var dst [26]byte
	dst[0] = crockford[(raw[0]&224)>>5]
	dst[1] = crockford[raw[0]&31]
	dst[2] = crockford[(raw[1]&248)>>3]
	dst[3] = crockford[((raw[1]&7)<<2)|((raw[2]&192)>>6)]
	dst[4] = crockford[(raw[2]&62)>>1]
	dst[5] = crockford[((raw[2]&1)<<4)|((raw[3]&240)>>4)]
	dst[6] = crockford[((raw[3]&15)<<1)|((raw[4]&128)>>7)]
	dst[7] = crockford[(raw[4]&124)>>2]
	dst[8] = crockford[((raw[4]&3)<<3)|((raw[5]&224)>>5)]
	dst[9] = crockford[raw[5]&31]
	dst[10] = crockford[(raw[6]&248)>>3]
	dst[11] = crockford[((raw[6]&7)<<2)|((raw[7]&192)>>6)]
	dst[12] = crockford[(raw[7]&62)>>1]
	dst[13] = crockford[((raw[7]&1)<<4)|((raw[8]&240)>>4)]
	dst[14] = crockford[((raw[8]&15)<<1)|((raw[9]&128)>>7)]
	dst[15] = crockford[(raw[9]&124)>>2]
	dst[16] = crockford[((raw[9]&3)<<3)|((raw[10]&224)>>5)]
	dst[17] = crockford[raw[10]&31]
	dst[18] = crockford[(raw[11]&248)>>3]
	dst[19] = crockford[((raw[11]&7)<<2)|((raw[12]&192)>>6)]
	dst[20] = crockford[(raw[12]&62)>>1]
	dst[21] = crockford[((raw[12]&1)<<4)|((raw[13]&240)>>4)]
	dst[22] = crockford[((raw[13]&15)<<1)|((raw[14]&128)>>7)]
	dst[23] = crockford[(raw[14]&124)>>2]
	dst[24] = crockford[((raw[14]&3)<<3)|((raw[15]&224)>>5)]
	dst[25] = crockford[raw[15]&31]
	return string(dst[:])
}
