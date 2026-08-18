package main

// SNMP v3 USM（User-based Security Model）：认证 + 加密。
//
// 安全说明：MD5 / SHA-1 / DES 在通用场景下是弱算法，但 SNMPv3 USM 的 RFC 3414 / 3826 /
// 7860 明文规定用它们做认证与加密，为与海量存量网络设备互通，这里**必须**实现，属协议
// 强约束而非我方选择。gosec 对 crypto/md5·sha1·des 的告警用 #nosec 标注放行。
//
// 流程：engine discovery（拿 engineID/boots/time）→ 口令派生 Ku（1MB 扩展）→ 本地化 Kul
// （H(Ku‖engineID‖Ku)）→ 认证 HMAC-*-96（占位清零→整包序列化→HMAC→原位覆盖）→
// 加密 DES-CBC / AES-128-CFB → 报文编排（ScopedPDU/USM/HeaderData/Whole）。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des" // #nosec G502 -- SNMPv3 USM 协议强制，需与存量设备互通
	"crypto/hmac"
	"crypto/md5"  // #nosec G501 -- SNMPv3 USM 认证协议强制
	"crypto/sha1" // #nosec G505 -- SNMPv3 USM 认证协议强制
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net"
	"sync/atomic"
	"time"
)

// ----------------------------------------------------------------------------
// 结构
// ----------------------------------------------------------------------------

// usmUser 是一个 v3 用户的安全凭据（从 SNMPTarget 解析而来）。
type usmUser struct {
	name      string
	secLevel  int    // 0 noAuthNoPriv / 1 authNoPriv / 3 authPriv
	authProto string // "MD5" | "SHA" | "SHA256"
	authPass  []byte
	privProto string // "DES" | "AES"
	privPass  []byte
	ctxName   string
}

// engineEntry 缓存一个 authoritative engine 的发现结果与本地化密钥。
type engineEntry struct {
	id       []byte
	boots    int32
	time     int32
	syncedAt time.Time
	authKul  []byte
	privKul  []byte
}

// estimatedTime 用本地墙钟推进 engineTime（发现后每过 1 秒 +1）。
func (e *engineEntry) estimatedTime() int32 {
	if e.syncedAt.IsZero() {
		return e.time
	}
	return e.time + int32(time.Since(e.syncedAt).Seconds())
}

// 全局 salt 计数器（privacy IV 用），启动播种后原子递增。
var usmSaltCounter atomic.Uint64

func init()            { usmSaltCounter.Store(uint64(time.Now().UnixNano())) }
func nextSalt() uint64 { return usmSaltCounter.Add(1) }

// ----------------------------------------------------------------------------
// 认证哈希选择
// ----------------------------------------------------------------------------

func newAuthHashFn(proto string) func() hash.Hash {
	switch proto {
	case "MD5":
		return md5.New
	case "SHA", "SHA1":
		return sha1.New
	case "SHA256":
		return sha256.New
	default:
		return nil
	}
}

func newAuthHash(proto string) hash.Hash {
	fn := newAuthHashFn(proto)
	if fn == nil {
		return nil
	}
	return fn()
}

// authParamLen 是 msgAuthenticationParameters 的截断长度。
func authParamLen(proto string) int {
	switch proto {
	case "MD5", "SHA", "SHA1":
		return 12 // HMAC-*-96
	case "SHA256":
		return 24 // usmHMAC192SHA256（RFC 7860）
	default:
		return 12
	}
}

// ----------------------------------------------------------------------------
// 口令派生 + 密钥本地化（RFC 3414 A.2）
// ----------------------------------------------------------------------------

// passwordToKey 把口令循环填满 1MB 做一次哈希得到 Ku（用 64 字节窗口喂 hash，不开大数组）。
func passwordToKey(proto string, password []byte) []byte {
	h := newAuthHash(proto)
	if h == nil || len(password) == 0 {
		return nil
	}
	var buf [64]byte
	plen := len(password)
	idx := 0
	for count := 0; count < 1048576; count += 64 {
		for i := 0; i < 64; i++ {
			buf[i] = password[idx%plen]
			idx++
		}
		h.Write(buf[:])
	}
	return h.Sum(nil)
}

// localizeKey 把 Ku 本地化到指定 engine：Kul = H(Ku ‖ engineID ‖ Ku)。
func localizeKey(proto string, ku, engineID []byte) []byte {
	h := newAuthHash(proto)
	if h == nil {
		return nil
	}
	h.Write(ku)
	h.Write(engineID)
	h.Write(ku)
	return h.Sum(nil)
}

// deriveKeys 根据用户凭据与 engineID 算出并缓存本地化认证/加密密钥。
func (e *engineEntry) deriveKeys(u *usmUser) {
	if u.secLevel >= 1 {
		e.authKul = localizeKey(u.authProto, passwordToKey(u.authProto, u.authPass), e.id)
	}
	if u.secLevel >= 3 {
		// 加密密钥同样用**认证协议**的哈希派生（RFC 3414/3826 约定）。
		e.privKul = localizeKey(u.authProto, passwordToKey(u.authProto, u.privPass), e.id)
	}
}

// ----------------------------------------------------------------------------
// 认证 HMAC
// ----------------------------------------------------------------------------

// computeAuth 对整条报文算 HMAC 并截断到协议长度。
func computeAuth(proto string, authKey, wholeMsg []byte) []byte {
	fn := newAuthHashFn(proto)
	if fn == nil {
		return nil
	}
	mac := hmac.New(fn, authKey)
	mac.Write(wholeMsg)
	sum := mac.Sum(nil)
	n := authParamLen(proto)
	if n > len(sum) {
		n = len(sum)
	}
	return sum[:n]
}

// ----------------------------------------------------------------------------
// 加解密
// ----------------------------------------------------------------------------

// encryptDES 用 DES-CBC 加密 ScopedPDU。key=privKul[0:8]，pre-IV=privKul[8:16]，
// salt=boots(4)‖counter(4)，IV=preIV XOR salt。privParams 返回 salt(8B)。
func encryptDES(privKul []byte, boots int32, saltCounter uint32, plaintext []byte) (ct, privParams []byte, err error) {
	if len(privKul) < 16 {
		return nil, nil, errors.New("snmp v3: DES 需要 ≥16 字节本地化密钥")
	}
	desKey := privKul[0:8]
	preIV := privKul[8:16]
	salt := make([]byte, 8)
	binary.BigEndian.PutUint32(salt[0:4], uint32(boots))
	binary.BigEndian.PutUint32(salt[4:8], saltCounter)
	iv := make([]byte, 8)
	for i := 0; i < 8; i++ {
		iv[i] = preIV[i] ^ salt[i]
	}
	block, err := des.NewCipher(desKey) // #nosec G405 -- USM 协议强制
	if err != nil {
		return nil, nil, err
	}
	// 补齐到 8 字节整数倍（接收端按 ScopedPDU 自身长度识别有效负载，尾部补位被忽略）。
	pt := plaintext
	if pad := (8 - len(pt)%8) % 8; pad > 0 {
		pt = append(append([]byte(nil), pt...), make([]byte, pad)...)
	}
	out := make([]byte, len(pt))
	// #nosec G407 -- IV 非硬编码：按 RFC 3826 每报文由 preIV XOR (engineBoots‖salt) 派生
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, pt)
	return out, salt, nil
}

// decryptDES 反向：IV = preIV XOR privParams(salt)。
func decryptDES(privKul, privParams, ct []byte) ([]byte, error) {
	if len(privKul) < 16 {
		return nil, errors.New("snmp v3: DES 密钥过短")
	}
	if len(privParams) != 8 || len(ct)%8 != 0 || len(ct) == 0 {
		return nil, errors.New("snmp v3: DES 密文/salt 长度非法")
	}
	preIV := privKul[8:16]
	iv := make([]byte, 8)
	for i := 0; i < 8; i++ {
		iv[i] = preIV[i] ^ privParams[i]
	}
	block, err := des.NewCipher(privKul[0:8]) // #nosec G405 -- USM 协议强制
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return out, nil
}

// encryptAES 用 AES-128-CFB 加密。key=privKul[0:16]，IV=boots(4)‖time(4)‖salt(8)，
// 流模式免补齐。privParams 返回 salt(8B)。
func encryptAES(privKul []byte, boots, etime int32, saltVal uint64, plaintext []byte) (ct, privParams []byte, err error) {
	if len(privKul) < 16 {
		return nil, nil, errors.New("snmp v3: AES 需要 ≥16 字节本地化密钥")
	}
	iv := make([]byte, 16)
	binary.BigEndian.PutUint32(iv[0:4], uint32(boots))
	binary.BigEndian.PutUint32(iv[4:8], uint32(etime))
	binary.BigEndian.PutUint64(iv[8:16], saltVal)
	block, err := aes.NewCipher(privKul[0:16])
	if err != nil {
		return nil, nil, err
	}
	out := make([]byte, len(plaintext))
	// #nosec G407 -- IV 非硬编码：按 RFC 3826 由 engineBoots‖engineTime‖salt 每报文派生
	//lint:ignore SA1019 RFC 3826 defines SNMPv3 AES privacy as CFB-128; CTR/AEAD would not interoperate
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(out, plaintext)
	privParams = make([]byte, 8)
	binary.BigEndian.PutUint64(privParams, saltVal)
	return out, privParams, nil
}

// decryptAES 反向：IV 用响应报文里的 boots/time + privParams(salt)。
func decryptAES(privKul []byte, boots, etime int32, privParams, ct []byte) ([]byte, error) {
	if len(privKul) < 16 {
		return nil, errors.New("snmp v3: AES 密钥过短")
	}
	if len(privParams) != 8 {
		return nil, errors.New("snmp v3: AES salt 长度非法")
	}
	iv := make([]byte, 16)
	binary.BigEndian.PutUint32(iv[0:4], uint32(boots))
	binary.BigEndian.PutUint32(iv[4:8], uint32(etime))
	copy(iv[8:16], privParams)
	block, err := aes.NewCipher(privKul[0:16])
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	//lint:ignore SA1019 RFC 3826 defines SNMPv3 AES privacy as CFB-128; CTR/AEAD would not interoperate
	cipher.NewCFBDecrypter(block, iv).XORKeyStream(out, ct)
	return out, nil
}

func (u *usmUser) encrypt(e *engineEntry, plaintext []byte) (ct, privParams []byte, err error) {
	switch u.privProto {
	case "DES":
		return encryptDES(e.privKul, e.boots, uint32(nextSalt()), plaintext)
	case "AES", "AES128":
		return encryptAES(e.privKul, e.boots, e.estimatedTime(), nextSalt(), plaintext)
	default:
		return nil, nil, fmt.Errorf("snmp v3: 不支持的加密协议 %q", u.privProto)
	}
}

func (u *usmUser) decrypt(boots, etime int32, privKul, privParams, ct []byte) ([]byte, error) {
	switch u.privProto {
	case "DES":
		return decryptDES(privKul, privParams, ct)
	case "AES", "AES128":
		return decryptAES(privKul, boots, etime, privParams, ct)
	default:
		return nil, fmt.Errorf("snmp v3: 不支持的加密协议 %q", u.privProto)
	}
}

// ----------------------------------------------------------------------------
// 报文编排
// ----------------------------------------------------------------------------

const msgMaxSize = 65507

// buildV3Message 编排一条 v3 请求报文并（authPriv/authNoPriv 时）就地写入 HMAC。
func (u *usmUser) buildV3Message(e *engineEntry, msgID int32, pduBytes []byte) ([]byte, error) {
	scoped := encodeTLV(tagSequence, concat(
		encodeOctetString(e.id),
		encodeOctetString([]byte(u.ctxName)),
		pduBytes,
	))

	var msgData, privParams []byte
	flags := byte(0x04) // reportable
	if u.secLevel >= 1 {
		flags |= 0x01 // auth
	}
	if u.secLevel >= 3 {
		flags |= 0x02 // priv
		enc, pp, err := u.encrypt(e, scoped)
		if err != nil {
			return nil, err
		}
		msgData = encodeOctetString(enc)
		privParams = pp
	} else {
		msgData = scoped
	}

	authPlaceholder := make([]byte, 0)
	if u.secLevel >= 1 {
		authPlaceholder = make([]byte, authParamLen(u.authProto))
	}
	usm := encodeTLV(tagSequence, concat(
		encodeOctetString(e.id),
		encodeInteger(int64(e.boots)),
		encodeInteger(int64(e.estimatedTime())),
		encodeOctetString([]byte(u.name)),
		encodeOctetString(authPlaceholder),
		encodeOctetString(privParams),
	))
	header := encodeTLV(tagSequence, concat(
		encodeInteger(int64(msgID)),
		encodeInteger(msgMaxSize),
		encodeOctetString([]byte{flags}),
		encodeInteger(3), // msgSecurityModel = USM
	))
	whole := encodeTLV(tagSequence, concat(
		encodeInteger(3), // version
		header,
		encodeOctetString(usm),
		msgData,
	))

	if u.secLevel >= 1 {
		authP, err := locateAuthParams(whole)
		if err != nil {
			return nil, err
		}
		mac := computeAuth(u.authProto, e.authKul, whole)
		if len(mac) != len(authP) {
			return nil, errors.New("snmp v3: HMAC 长度与占位不符")
		}
		copy(authP, mac) // 原位覆盖（authP 别名 whole 底层数组）
	}
	return whole, nil
}

// locateAuthParams 重新 parse 刚生成的报文，定位 msgAuthenticationParameters 的字节切片
// （别名同一底层数组），供原位写入 HMAC——比手算绝对偏移稳。
func locateAuthParams(msg []byte) ([]byte, error) {
	_, body, _, err := readTLV(msg) // 外层 SEQUENCE
	if err != nil {
		return nil, err
	}
	_, _, rest, err := readTLV(body) // version
	if err != nil {
		return nil, err
	}
	_, _, rest, err = readTLV(rest) // HeaderData
	if err != nil {
		return nil, err
	}
	tag, usmOctet, _, err := readTLV(rest) // msgSecurityParameters (OCTET STRING)
	if err != nil || tag != tagOctetString {
		return nil, errors.New("snmp v3: 无 msgSecurityParameters")
	}
	_, usmBody, _, err := readTLV(usmOctet) // USM SEQUENCE
	if err != nil {
		return nil, err
	}
	r := usmBody
	for i := 0; i < 4; i++ { // 跳过 engineID/boots/time/userName
		if _, _, r, err = readTLV(r); err != nil {
			return nil, err
		}
	}
	tag, authP, _, err := readTLV(r) // authParams
	if err != nil || tag != tagOctetString {
		return nil, errors.New("snmp v3: 无 authParams")
	}
	return authP, nil
}

// v3EngineParams 从任意 v3 报文的 USM 里抽出 engineID/boots/time/privParams。
type v3SecParams struct {
	engineID   []byte
	boots      int32
	time       int32
	privParams []byte
	flags      byte
}

func parseV3SecParams(msg []byte) (v3SecParams, []byte, error) {
	var sp v3SecParams
	_, body, _, err := readTLV(msg)
	if err != nil {
		return sp, nil, err
	}
	_, _, rest, err := readTLV(body) // version
	if err != nil {
		return sp, nil, err
	}
	_, hdr, rest, err := readTLV(rest) // HeaderData
	if err != nil {
		return sp, nil, err
	}
	// HeaderData: msgID, msgMaxSize, msgFlags, msgSecurityModel
	_, _, hr, err := readTLV(hdr) // msgID
	if err != nil {
		return sp, nil, err
	}
	_, _, hr, err = readTLV(hr) // msgMaxSize
	if err != nil {
		return sp, nil, err
	}
	_, flagsB, _, err := readTLV(hr) // msgFlags
	if err != nil {
		return sp, nil, err
	}
	if len(flagsB) > 0 {
		sp.flags = flagsB[0]
	}
	tag, usmOctet, rest, err := readTLV(rest) // msgSecurityParameters
	if err != nil || tag != tagOctetString {
		return sp, nil, errors.New("snmp v3: 无 secparams")
	}
	_, usmBody, _, err := readTLV(usmOctet)
	if err != nil {
		return sp, nil, err
	}
	_, eid, r, err := readTLV(usmBody) // engineID
	if err != nil {
		return sp, nil, err
	}
	sp.engineID = append([]byte(nil), eid...)
	_, bc, r, err := readTLV(r) // boots
	if err != nil {
		return sp, nil, err
	}
	b, _ := decodeInteger(bc)
	sp.boots = int32(b)
	_, tc, r, err := readTLV(r) // time
	if err != nil {
		return sp, nil, err
	}
	tm, _ := decodeInteger(tc)
	sp.time = int32(tm)
	_, _, r, err = readTLV(r) // userName
	if err != nil {
		return sp, nil, err
	}
	_, _, r, err = readTLV(r) // authParams
	if err != nil {
		return sp, nil, err
	}
	_, pp, _, err := readTLV(r) // privParams
	if err != nil {
		return sp, nil, err
	}
	sp.privParams = append([]byte(nil), pp...)
	// rest 现在指向 msgData（scopedPDU 明文 或 加密 OCTET STRING）
	return sp, rest, nil
}

// ----------------------------------------------------------------------------
// v3Exchanger
// ----------------------------------------------------------------------------

type v3Exchanger struct {
	conn    net.Conn
	user    *usmUser
	engine  *engineEntry
	timeout time.Duration
	retries int
}

// newV3Exchanger 从 SNMPTarget 构造 v3 exchanger。
func newV3Exchanger(conn net.Conn, t SNMPTarget, timeout time.Duration, retries int) (exchanger, error) {
	u := &usmUser{
		name:      t.User,
		authProto: normalizeAuthProto(t.AuthProto),
		authPass:  []byte(t.resolveAuthPass()),
		privProto: normalizePrivProto(t.PrivProto),
		privPass:  []byte(t.resolvePrivPass()),
		ctxName:   t.ContextName,
	}
	u.secLevel = deriveSecLevel(t.SecLevel, u)
	if u.name == "" {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, errors.New("snmp v3: 缺少 user 字段")
	}
	if u.secLevel >= 1 && (u.authProto == "" || len(u.authPass) == 0) {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, errors.New("snmp v3: authNoPriv/authPriv 需要 auth_proto + auth_pass")
	}
	if u.secLevel >= 3 && (u.privProto == "" || len(u.privPass) == 0) {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, errors.New("snmp v3: authPriv 需要 priv_proto + priv_pass")
	}
	return &v3Exchanger{conn: conn, user: u, timeout: timeout, retries: retries}, nil
}

func normalizeAuthProto(s string) string {
	switch upper(s) {
	case "MD5":
		return "MD5"
	case "SHA", "SHA1":
		return "SHA"
	case "SHA256":
		return "SHA256"
	default:
		return ""
	}
}

func normalizePrivProto(s string) string {
	switch upper(s) {
	case "DES":
		return "DES"
	case "AES", "AES128":
		return "AES"
	default:
		return ""
	}
}

// deriveSecLevel 显式 sec_level 优先，否则按凭据存在性推断。
func deriveSecLevel(explicit string, u *usmUser) int {
	switch upper(explicit) {
	case "NOAUTHNOPRIV":
		return 0
	case "AUTHNOPRIV":
		return 1
	case "AUTHPRIV":
		return 3
	}
	switch {
	case u.authProto != "" && u.privProto != "":
		return 3
	case u.authProto != "":
		return 1
	default:
		return 0
	}
}

func upper(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		}
	}
	return string(out)
}

// discover 发送空 USM 请求拿 engineID/boots/time，并派生本地化密钥。
func (x *v3Exchanger) discover() error {
	msgID := nextRequestID()
	reqID := nextRequestID()
	inner := buildGet(reqID, nil) // 空 varbinds
	scoped := encodeTLV(tagSequence, concat(
		encodeOctetString(nil), // contextEngineID 空
		encodeOctetString(nil), // contextName 空
		inner,
	))
	usm := encodeTLV(tagSequence, concat(
		encodeOctetString(nil),                 // engineID 空
		encodeInteger(0),                       // boots
		encodeInteger(0),                       // time
		encodeOctetString([]byte(x.user.name)), // userName
		encodeOctetString(nil),                 // authParams 空
		encodeOctetString(nil),                 // privParams 空
	))
	header := encodeTLV(tagSequence, concat(
		encodeInteger(int64(msgID)),
		encodeInteger(msgMaxSize),
		encodeOctetString([]byte{0x04}), // reportable, noAuthNoPriv
		encodeInteger(3),
	))
	whole := encodeTLV(tagSequence, concat(encodeInteger(3), header, encodeOctetString(usm), scoped))

	resp, err := snmpExchange(x.conn, x.timeout, x.retries, whole)
	if err != nil {
		return fmt.Errorf("snmp v3 engine 发现失败: %w", err)
	}
	sp, _, err := parseV3SecParams(resp)
	if err != nil {
		return err
	}
	if len(sp.engineID) == 0 {
		return errors.New("snmp v3: 响应无 engineID")
	}
	e := &engineEntry{id: sp.engineID, boots: sp.boots, time: sp.time, syncedAt: time.Now()}
	e.deriveKeys(x.user)
	x.engine = e
	return nil
}

// request 发送一个 PDU（authNoPriv/authPriv 时带认证/加密），收响应并校验。
func (x *v3Exchanger) request(pduBytes []byte, reqID int32) (pdu, error) {
	if x.engine == nil || len(x.engine.id) == 0 {
		if err := x.discover(); err != nil {
			return pdu{}, err
		}
	}
	p, isReport, err := x.roundTrip(pduBytes)
	if err != nil {
		return pdu{}, err
	}
	if isReport {
		// 常见是 usmStatsNotInTimeWindows：重新发现同步时间窗后重试一次。
		x.engine = nil
		if err := x.discover(); err != nil {
			return pdu{}, err
		}
		p, isReport, err = x.roundTrip(pduBytes)
		if err != nil {
			return pdu{}, err
		}
		if isReport {
			return pdu{}, errors.New("snmp v3: 收到 Report(时间窗/认证/发现错误)")
		}
	}
	if p.requestID != reqID {
		return pdu{}, fmt.Errorf("snmp v3: request-id 不匹配(want %d got %d)", reqID, p.requestID)
	}
	return p, nil
}

// roundTrip 编排+发送+解析一次，返回 (pdu, 是否为 Report, err)。
func (x *v3Exchanger) roundTrip(pduBytes []byte) (pdu, bool, error) {
	msgID := nextRequestID()
	msg, err := x.user.buildV3Message(x.engine, msgID, pduBytes)
	if err != nil {
		return pdu{}, false, err
	}
	resp, err := snmpExchange(x.conn, x.timeout, x.retries, msg)
	if err != nil {
		return pdu{}, false, err
	}
	return x.parseResponse(resp)
}

// parseResponse 解析 v3 响应报文（必要时解密），返回内层 PDU 与是否 Report。
func (x *v3Exchanger) parseResponse(msg []byte) (pdu, bool, error) {
	sp, msgData, err := parseV3SecParams(msg)
	if err != nil {
		return pdu{}, false, err
	}
	var scopedBody []byte
	if sp.flags&0x02 != 0 { // 加密：msgData 是 OCTET STRING(密文)
		tag, ct, _, err := readTLV(msgData)
		if err != nil || tag != tagOctetString {
			return pdu{}, false, errors.New("snmp v3: 加密 msgData 非 OCTET STRING")
		}
		// 用响应自带的 boots/time 解密（authoritative engine 用它自己的时间做 IV）。
		plain, err := x.user.decrypt(sp.boots, sp.time, x.engine.privKul, sp.privParams, ct)
		if err != nil {
			return pdu{}, false, err
		}
		_, sb, _, err := readTLV(plain) // ScopedPDU SEQUENCE（尾部 DES 补位被自身长度忽略）
		if err != nil {
			return pdu{}, false, err
		}
		scopedBody = sb
	} else { // 明文：msgData 就是 ScopedPDU SEQUENCE
		tag, sb, _, err := readTLV(msgData)
		if err != nil || tag != tagSequence {
			return pdu{}, false, errors.New("snmp v3: 明文 msgData 非 SEQUENCE")
		}
		scopedBody = sb
	}
	// ScopedPDU: contextEngineID, contextName, pdu
	_, _, r, err := readTLV(scopedBody) // contextEngineID
	if err != nil {
		return pdu{}, false, err
	}
	_, _, r, err = readTLV(r) // contextName
	if err != nil {
		return pdu{}, false, err
	}
	pduTag, pduContent, _, err := readTLV(r) // pdu
	if err != nil {
		return pdu{}, false, err
	}
	p, err := parsePDU(pduTag, pduContent)
	return p, pduTag == pduReport, err
}

func (x *v3Exchanger) get(oids [][]uint32) (pdu, error) {
	reqID := nextRequestID()
	return x.request(buildGet(reqID, oids), reqID)
}

func (x *v3Exchanger) getBulk(nonRep, maxRep int, oids [][]uint32) (pdu, error) {
	reqID := nextRequestID()
	return x.request(buildGetBulk(reqID, nonRep, maxRep, oids), reqID)
}

func (x *v3Exchanger) close() {
	if x.conn != nil {
		_ = x.conn.Close()
	}
}
