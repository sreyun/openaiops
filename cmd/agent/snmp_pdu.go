package main

// SNMP PDU / 消息帧层：varbind、pdu 结构，GET/GETNEXT/GETBULK 构造，v2c 消息组装与解析。
// 只依赖 snmp_ber.go 的编解码原语；被轮询器（collector_snmp.go）与 trap 接收器
// （collector_snmp_trap.go）共用。

import (
	"fmt"
	"sync/atomic"
	"time"
)

// varbind 是一条 OID=value 绑定。
type varbind struct {
	oid   []uint32
	value snmpValue
}

// pdu 是解析后的 SNMP PDU（Response/Report/Trap 等）。
// 对 GETBULK 请求，errStatus 槽复用为 non-repeaters、errIndex 槽复用为 max-repetitions。
type pdu struct {
	pduType   byte
	requestID int32
	errStatus int
	errIndex  int
	varbinds  []varbind
}

// request-id 生成器：启动时用纳秒播种，之后原子递增。用于匹配响应、丢弃迟到重传。
var reqIDCounter atomic.Int32

func init() { reqIDCounter.Store(int32(time.Now().UnixNano() & 0x7fffffff)) }

// nextRequestID 返回一个非零正 request-id。
func nextRequestID() int32 {
	v := reqIDCounter.Add(1) & 0x7fffffff
	if v == 0 {
		v = 1
	}
	return v
}

// ----------------------------------------------------------------------------
// PDU 构造（请求）
// ----------------------------------------------------------------------------

// encodeVarbind 编码一条 varbind：SEQUENCE{ OID, value }。
func encodeVarbind(oid []uint32, valueTLV []byte) []byte {
	return encodeTLV(tagSequence, concat(encodeOID(oid), valueTLV))
}

// encodeNullVarbinds 编码请求用的 varbind-list（value 全 Null）。
func encodeNullVarbinds(oids [][]uint32) []byte {
	var vbs []byte
	for _, oid := range oids {
		vbs = append(vbs, encodeVarbind(oid, encodeNull())...)
	}
	return encodeTLV(tagSequence, vbs)
}

// buildRequestPDU 组装一个请求 PDU（未做 v2c/v3 外层封装）。
// field2/field3 对 GET/GETNEXT 是 error-status/error-index(恒 0)，对 GETBULK 是
// non-repeaters/max-repetitions。
func buildRequestPDU(pduType byte, reqID int32, field2, field3 int, oids [][]uint32) []byte {
	body := concat(
		encodeInteger(int64(reqID)),
		encodeInteger(int64(field2)),
		encodeInteger(int64(field3)),
		encodeNullVarbinds(oids),
	)
	return encodeTLV(pduType, body)
}

func buildGet(reqID int32, oids [][]uint32) []byte {
	return buildRequestPDU(pduGet, reqID, 0, 0, oids)
}

func buildGetBulk(reqID int32, nonRep, maxRep int, oids [][]uint32) []byte {
	return buildRequestPDU(pduGetBulk, reqID, nonRep, maxRep, oids)
}

// ----------------------------------------------------------------------------
// PDU 解析（响应）
// ----------------------------------------------------------------------------

// parsePDU 解析一个 PDU body（PDU tag 后面的内容）为 pdu 结构。
func parsePDU(pduType byte, content []byte) (pdu, error) {
	p := pdu{pduType: pduType}

	tag, c, rest, err := readTLV(content)
	if err != nil || tag != tagInteger {
		return p, fmt.Errorf("snmp pdu: 读 request-id 失败: %v", err)
	}
	rid, _ := decodeInteger(c)
	p.requestID = int32(rid)

	tag, c, rest, err = readTLV(rest)
	if err != nil || tag != tagInteger {
		return p, fmt.Errorf("snmp pdu: 读 error-status 失败: %v", err)
	}
	es, _ := decodeInteger(c)
	p.errStatus = int(es)

	tag, c, rest, err = readTLV(rest)
	if err != nil || tag != tagInteger {
		return p, fmt.Errorf("snmp pdu: 读 error-index 失败: %v", err)
	}
	ei, _ := decodeInteger(c)
	p.errIndex = int(ei)

	tag, vbContent, _, err := readTLV(rest)
	if err != nil || tag != tagSequence {
		return p, fmt.Errorf("snmp pdu: 读 varbind-list 失败: %v", err)
	}
	vbs, err := parseVarbinds(vbContent)
	if err != nil {
		return p, err
	}
	p.varbinds = vbs
	return p, nil
}

// parseVarbinds 把 varbind-list 内容解成 []varbind。
func parseVarbinds(content []byte) ([]varbind, error) {
	var out []varbind
	rest := content
	for len(rest) > 0 {
		tag, vbContent, r, err := readTLV(rest)
		if err != nil {
			return nil, err
		}
		rest = r
		if tag != tagSequence {
			return nil, fmt.Errorf("snmp pdu: varbind 非 SEQUENCE(tag %#x)", tag)
		}
		otag, oc, vrest, err := readTLV(vbContent)
		if err != nil || otag != tagOID {
			return nil, fmt.Errorf("snmp pdu: varbind OID 失败: %v", err)
		}
		oid, err := decodeOIDValue(oc)
		if err != nil {
			return nil, err
		}
		vtag, vc, _, err := readTLV(vrest)
		if err != nil {
			return nil, fmt.Errorf("snmp pdu: varbind value 失败: %v", err)
		}
		val, err := decodeValue(vtag, vc)
		if err != nil {
			return nil, err
		}
		out = append(out, varbind{oid: oid, value: val})
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// v2c 消息帧
// ----------------------------------------------------------------------------

// buildV2CMessage 把一个 PDU 封装成完整 v2c 报文：SEQUENCE{ version=1, community, pdu }。
func buildV2CMessage(community string, pduBytes []byte) []byte {
	body := concat(
		encodeInteger(1), // version 1 = SNMPv2c
		encodeOctetString([]byte(community)),
		pduBytes,
	)
	return encodeTLV(tagSequence, body)
}

// parseMessageHeader 读外层 SEQUENCE 与 version，返回 version 与 version 之后的剩余字节。
// trap 接收器先用它判版本再分派。
func parseMessageHeader(b []byte) (version int, rest []byte, err error) {
	tag, content, _, err := readTLV(b)
	if err != nil || tag != tagSequence {
		return 0, nil, fmt.Errorf("snmp msg: 外层非 SEQUENCE: %v", err)
	}
	vtag, vc, r, err := readTLV(content)
	if err != nil || vtag != tagInteger {
		return 0, nil, fmt.Errorf("snmp msg: 读 version 失败: %v", err)
	}
	v, _ := decodeInteger(vc)
	return int(v), r, nil
}

// parseV2CMessage 解析完整 v2c 报文 → (version, community, pdu)。
func parseV2CMessage(b []byte) (version int, community string, p pdu, err error) {
	ver, rest, err := parseMessageHeader(b)
	if err != nil {
		return 0, "", pdu{}, err
	}
	ctag, cc, prest, err := readTLV(rest)
	if err != nil || ctag != tagOctetString {
		return ver, "", pdu{}, fmt.Errorf("snmp msg: 读 community 失败: %v", err)
	}
	ptag, pc, _, err := readTLV(prest)
	if err != nil {
		return ver, string(cc), pdu{}, fmt.Errorf("snmp msg: 读 pdu 失败: %v", err)
	}
	p, err = parsePDU(ptag, pc)
	return ver, string(cc), p, err
}
