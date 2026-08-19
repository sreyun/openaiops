package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"strconv"
	"strings"
)

// zmMaxFileBytes caps how much file data a single ZMODEM transfer may buffer in
// memory, preventing a huge (or maliciously crafted) sz stream from OOM-ing the
// agent. Matches the 100MB button-based transfer limit.
const zmMaxFileBytes = 100 << 20

// ZMODEM protocol handler — minimal but functional implementation for sz/rz.
//
// Handles the common frame types needed for file transfer:
//   ZRQINIT / ZRINIT / ZSINIT — session init
//   ZFILE / ZRPOS — file metadata & position
//   ZDATA / ZACK — data transfer
//   ZEOF / ZFIN — session end
//
// Both hex headers (**\x18B...) and binary headers (**\x18A... / **\x18C...)
// are supported. Subpacket CRC uses CRC-16 XMODEM.

// ---- ZMODEM constants ----

const (
	ZPAD = '*'  // pad character
	ZDLE = 0x18 // data link escape
	XON  = 0x11
	XOFF = 0x13
)

// Header format types
const (
	ZHEX   = 'B' // hex header
	ZBIN   = 'A' // binary-16 header
	ZBIN32 = 'C' // binary-32 header
)

// Frame types
const (
	ZRQINIT = 0
	ZRINIT  = 1
	ZSINIT  = 2
	ZACK    = 3
	ZFILE   = 4
	ZSKIP   = 5
	ZNAK    = 6
	ZABORT  = 7
	ZFIN    = 8
	ZRPOS   = 9
	ZDATA   = 10
	ZEOF    = 11
	ZFERR   = 12
	ZCRC    = 13
)

// Subpacket end types (within ZDATA frames)
const (
	ZCRCE = 'h' // CRC end
	ZCRCG = 'i' // CRC go
	ZCRCQ = 'j' // CRC query
	ZCRCW = 'k' // CRC wait
)

// ZMODEM header prefixes
var (
	hexHeaderPrefix = []byte{ZPAD, ZPAD, ZDLE, ZHEX} // **\x18B
	bin16Prefix     = []byte{ZPAD, ZDLE, ZBIN}       // *\x18A
	bin32Prefix     = []byte{ZPAD, ZDLE, ZBIN32}     // *\x18C
)

// HasZmodemHeader checks if data contains a ZMODEM header prefix.
func HasZmodemHeader(data []byte) bool {
	return bytes.Contains(data, hexHeaderPrefix) ||
		bytes.Contains(data, bin16Prefix) ||
		bytes.Contains(data, bin32Prefix)
}

// IndexZmodemHeader returns the index of the first ZMODEM header prefix in data,
// or -1 if none found.
func IndexZmodemHeader(data []byte) int {
	for _, prefix := range [][]byte{hexHeaderPrefix, bin16Prefix, bin32Prefix} {
		if idx := bytes.Index(data, prefix); idx >= 0 {
			return idx
		}
	}
	return -1
}

// ---- ZMODEM frame representation ----

// ZmFrame is a parsed ZMODEM frame.
type ZmFrame struct {
	Type  byte
	Flags [4]byte // ZP0-ZP3 (little-endian: f0,f1,f2,f3)
	Data  []byte  // payload (hex-decoded or raw)
}

// ZFileInfo is the metadata extracted from a ZFILE frame.
type ZFileInfo struct {
	Name   string
	Size   int64
	Mtime  int64
	Mode   uint32
	Serial int
}

// ---- Frame parsing ----

// parseZmFrame parses a ZMODEM frame from the beginning of data.
// Returns the frame, number of bytes consumed, and error.
// Returns nil, 0, nil if data doesn't start with a ZMODEM header.
func parseZmFrame(data []byte) (*ZmFrame, int, error) {
	if len(data) < 5 {
		return nil, 0, nil
	}
	// Try hex header first (most common)
	if bytes.HasPrefix(data, hexHeaderPrefix) {
		return parseHexFrame(data)
	}
	// Try binary-16 header
	if len(data) >= 4 && bytes.HasPrefix(data, bin16Prefix) {
		return parseBinFrame(data, false)
	}
	// Try binary-32 header
	if len(data) >= 4 && bytes.HasPrefix(data, bin32Prefix) {
		return parseBinFrame(data, true)
	}
	return nil, 0, nil
}

// parseHexFrame parses a hex-encoded ZMODEM header.
// Format: **\x18B [type:2][flags:8][payload:hex...] CR LF [XON]
func parseHexFrame(data []byte) (*ZmFrame, int, error) {
	// Skip the 4-byte prefix
	pos := 4

	// Find CR LF terminator
	crlfPos := bytes.Index(data[pos:], []byte{'\r', '\n'})
	if crlfPos < 0 {
		return nil, 0, nil // header not complete
	}
	hexPart := data[pos : pos+crlfPos]

	// Decode hex
	raw := make([]byte, hex.DecodedLen(len(hexPart)))
	n, err := hex.Decode(raw, hexPart)
	if err != nil {
		return nil, 0, fmt.Errorf("zmodem hex decode: %w", err)
	}
	raw = raw[:n]
	if len(raw) < 1 {
		return nil, 0, fmt.Errorf("zmodem hex frame too short")
	}

	f := &ZmFrame{Type: raw[0]}
	if len(raw) >= 5 {
		copy(f.Flags[:], raw[1:5])
	}
	if len(raw) > 5 {
		f.Data = make([]byte, len(raw)-5)
		copy(f.Data, raw[5:])
	}

	consumed := pos + crlfPos + 2 // +2 for CR LF
	// Skip optional XON
	if consumed < len(data) && data[consumed] == XON {
		consumed++
	}
	return f, consumed, nil
}

// parseBinFrame parses a binary ZMODEM header.
// Format: *\x18A [type][flags:4] [payload...] [crc:2]  (bin-16)
//
//	*\x18C [type][flags:4] [payload...] [crc:4]  (bin-32)
//
// For ZDATA frames, also extracts subpacket data from the bytes following
// the binary header. ZDATA subpackets are: ZDLE [subType] [data] [CRC16:2].
func parseBinFrame(data []byte, is32 bool) (*ZmFrame, int, error) {
	headerLen := 3 // *\x18 + type byte
	if len(data) < headerLen+4 {
		return nil, 0, nil
	}

	pos := 3
	typ := data[pos]
	pos++
	if pos+4 > len(data) {
		return nil, 0, nil
	}
	var flags [4]byte
	copy(flags[:], data[pos:pos+4])
	pos += 4

	// The payload extends until we find a valid subpacket header or end of data.
	// Binary headers don't have an explicit length; data is terminated by a
	// ZDLE escape followed by a subpacket end type.
	// For simplicity, we read the payload until the next ZDLE or end of data.
	payloadStart := pos
	crcLen := 2
	if is32 {
		crcLen = 4
	}
	// Binary header: payload is between flags and CRC
	// For ZFILE, the payload is the file metadata (null-separated)
	// For ZDATA, there's usually no payload in the header (data follows in subpackets)
	// We need to find the end of the payload.
	// The CRC follows the payload. But we don't know the payload length.
	// Strategy: scan for ZDLE followed by ZCRCE/ZCRCG/ZCRCQ/ZCRCW (subpacket end)
	// or for the next header prefix.
	payloadEnd := pos
	for payloadEnd < len(data)-crcLen {
		// Check for ZDLE + subpacket end
		if data[payloadEnd] == ZDLE && payloadEnd+1 < len(data) {
			next := data[payloadEnd+1]
			if next == ZCRCE || next == ZCRCG || next == ZCRCQ || next == ZCRCW {
				break
			}
		}
		payloadEnd++
	}

	payload := data[payloadStart:payloadEnd]
	consumed := payloadEnd + crcLen

	f := &ZmFrame{Type: typ, Flags: flags}
	if len(payload) > 0 {
		f.Data = make([]byte, len(payload))
		copy(f.Data, payload)
	}

	// For ZDATA frames, extract subpacket data from the bytes following the
	// binary header. The subpackets are: ZDLE [subType] [fileData] [CRC16:2].
	// Without this, sz downloads silently produce zero-byte files.
	if typ == ZDATA {
		subData, subConsumed := extractSubpacketsFromStream(data[consumed:])
		if len(subData) > 0 {
			f.Data = append(f.Data, subData...)
		}
		consumed += subConsumed
	}

	return f, consumed, nil
}

// extractSubpacketsFromStream extracts file data from ZDATA subpackets in a
// raw byte stream. Each subpacket is: ZDLE [subType] [data] [CRC16:2].
// Returns extracted file data and total bytes consumed.
func extractSubpacketsFromStream(data []byte) (fileData []byte, consumed int) {
	pos := 0
	for pos < len(data) {
		// Find ZDLE
		zdleIdx := bytes.IndexByte(data[pos:], ZDLE)
		if zdleIdx < 0 {
			break
		}
		pos += zdleIdx
		if pos+1 >= len(data) {
			break
		}
		subType := data[pos+1]
		// Check if it's a valid subpacket type
		if subType != ZCRCE && subType != ZCRCG && subType != ZCRCQ && subType != ZCRCW {
			// Not a valid subpacket — might be start of next frame
			break
		}
		// Data starts at pos+2, ends at next ZDLE (minus CRC16) or end of data (minus CRC16)
		dataStart := pos + 2
		if dataStart+2 > len(data) {
			break // not enough data for CRC16
		}
		// Find the next ZDLE to determine the end of this subpacket
		nextZdle := bytes.IndexByte(data[dataStart:], ZDLE)
		var dataEnd int
		if nextZdle < 0 {
			// Last subpacket — data extends to end-2 (CRC16)
			dataEnd = len(data) - 2
		} else {
			// Data extends to the next ZDLE position minus 2 (CRC16)
			dataEnd = dataStart + nextZdle - 2
		}
		if dataEnd > dataStart {
			fileData = append(fileData, data[dataStart:dataEnd]...)
		}
		// Move past this subpacket
		if nextZdle < 0 {
			consumed = len(data)
			break
		}
		pos = dataStart + nextZdle
		consumed = pos
	}
	return fileData, consumed
}

// ---- Frame building ----

// buildHexFrame builds a hex-encoded ZMODEM frame.
// Returns the complete frame bytes including CR LF and XON.
func buildHexFrame(typ byte, flags [4]byte, data []byte) []byte {
	// Build the hex-encoded part: type(1) + flags(4) + data(N)
	raw := make([]byte, 1+4+len(data))
	raw[0] = typ
	copy(raw[1:5], flags[:])
	copy(raw[5:], data)

	hexStr := hex.EncodeToString(raw)

	// Frame: **\x18B + hex + CR + LF + XON
	var buf bytes.Buffer
	buf.Write(hexHeaderPrefix)
	buf.WriteString(hexStr)
	buf.WriteByte('\r')
	buf.WriteByte('\n')
	buf.WriteByte(XON)
	return buf.Bytes()
}

// buildBin16Header builds a binary-16 header (without subpacket data).
func buildBin16Header(typ byte, flags [4]byte) []byte {
	buf := []byte{ZPAD, ZDLE, ZBIN, typ}
	buf = append(buf, flags[:]...)
	return buf
}

// buildZdataPacket builds a ZDATA subpacket: ZDLE + type + data + CRC16.
func buildZdataPacket(subType byte, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(ZDLE)
	buf.WriteByte(subType)
	buf.Write(data)
	crc := crc16Xmodem(data)
	crcBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(crcBytes, crc)
	buf.Write(crcBytes)
	return buf.Bytes()
}

// buildZackFrame builds a ZACK frame (hex header, no data).
func buildZackFrame(pos [4]byte) []byte {
	return buildHexFrame(ZACK, pos, nil)
}

// buildZrinitFrame builds a ZRINIT frame.
func buildZrinitFrame() []byte {
	// ZRINIT flags: 4 bytes of receive capabilities
	// For simplicity, we use standard flags
	var flags [4]byte
	flags[0] = 4 // CANFDW (full-duplex)
	return buildHexFrame(ZRINIT, flags, nil)
}

// buildZfinFrame builds a ZFIN frame.
func buildZfinFrame() []byte {
	var flags [4]byte
	return buildHexFrame(ZFIN, flags, nil)
}

// buildZfileFrame builds a ZFILE frame with file metadata.
func buildZfileFrame(name string, size int64) []byte {
	// ZFILE payload: filename\0size mode serial mtime...
	// Format: name\0 length mode serial mtime...
	var buf bytes.Buffer
	buf.WriteString(name)
	buf.WriteByte(0)
	fmt.Fprintf(&buf, "%d", size)
	buf.WriteByte(0)
	buf.WriteString("0") // mode
	buf.WriteByte(0)
	buf.WriteString("0") // serial
	buf.WriteByte(0)
	buf.WriteString("0") // mtime
	buf.WriteByte(0)

	var flags [4]byte
	return buildHexFrame(ZFILE, flags, buf.Bytes())
}

// buildZeofFrame builds a ZEOF frame.
func buildZeofFrame() []byte {
	var flags [4]byte
	return buildHexFrame(ZEOF, flags, nil)
}

// ---- CRC-16 XMODEM ----

var crc16Table [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		crc := uint16(i) << 8
		for j := 0; j < 8; j++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
		crc16Table[i] = crc
	}
}

func crc16Xmodem(data []byte) uint16 {
	crc := uint16(0)
	for _, b := range data {
		crc = crc16Table[(byte(crc>>8)^b)&0xFF] ^ (crc << 8)
	}
	return crc
}

// ---- ZMODEM session state machine ----

// zmState tracks the state of a ZMODEM session.
type zmState int

const (
	zmIdle zmState = iota
	zmInit zmState = iota // waiting for ZRQINIT
	zmFile zmState = iota // file info received
	zmData zmState = iota // receiving/sending data
	zmEnd  zmState = iota // session ending
)

// ZmSession manages a ZMODEM file transfer session.
type ZmSession struct {
	State zmState

	// File metadata (from ZFILE frame)
	File *ZFileInfo

	// Accumulated file data (for download)
	DataBuf bytes.Buffer

	// For upload: data to send, split into chunks
	UploadData       []byte
	UploadChunkSz    int
	UploadOffset     int
	UploadFirstChunk bool // true when binary header needs to be sent before first subpacket

	// CRC-32 for file integrity check
	CRC32 uint32

	// Subpacket data buffer (accumulates data from ZDATA subpackets)
	subBuf bytes.Buffer
}

// NewZmSession creates a new ZMODEM session.
func NewZmSession() *ZmSession {
	return &ZmSession{UploadChunkSz: 1024}
}

// Reset resets the session for a new transfer.
func (s *ZmSession) Reset() {
	s.State = zmInit
	s.File = nil
	s.DataBuf.Reset()
	s.UploadData = nil
	s.UploadOffset = 0
	s.CRC32 = 0
	s.subBuf.Reset()
}

// HandleFrame processes a ZMODEM frame and returns the response frame(s).
// For download: extracts file data from ZDATA frames.
// Returns a slice of response frames to write to PTY stdin.
func (s *ZmSession) HandleFrame(f *ZmFrame) [][]byte {
	var responses [][]byte

	switch f.Type {
	case ZRQINIT:
		// Remote side wants to start a session
		s.State = zmInit
		responses = append(responses, buildZrinitFrame())

	case ZSINIT:
		// Remote side is sending (sz)
		s.State = zmInit

	case ZFILE:
		// File metadata
		s.File = parseZFileInfo(f.Data)
		s.State = zmFile
		s.DataBuf.Reset()
		s.CRC32 = 0
		// Send ZRPOS(0) — start from beginning
		responses = append(responses, buildHexFrame(ZRPOS, [4]byte{}, nil))

	case ZDATA:
		// File data — may be raw (from binary frame with subpackets already
		// extracted) or subpacket-wrapped (from hex frame).
		s.State = zmData
		data := extractZdataPayload(f.Data)
		if len(data) == 0 {
			// No subpacket structure found — use raw data directly
			// (binary ZDATA frames already have subpackets extracted by parseBinFrame)
			data = f.Data
		}
		if len(data) > 0 {
			// Cap in-memory accumulation so a huge/malicious transfer can't OOM
			// the agent. On overflow, abort the session and tell the peer to stop.
			if s.DataBuf.Len()+len(data) > zmMaxFileBytes {
				slog.Warn("ZMODEM 传输超过大小上限，已中止", "limit", zmMaxFileBytes, "received", s.DataBuf.Len())
				s.Reset()
				return [][]byte{buildZfinFrame()}
			}
			s.DataBuf.Write(data)
			s.CRC32 = crc32.Update(s.CRC32, crc32.IEEETable, data)
		}
		// Send ZACK
		responses = append(responses, buildZackFrame(f.Flags))

	case ZEOF:
		// End of file
		s.State = zmEnd
		// Send ZRINIT to acknowledge
		responses = append(responses, buildZrinitFrame())

	case ZFIN:
		// Session finished
		responses = append(responses, buildZfinFrame())
		s.State = zmIdle

	case ZRINIT:
		// Remote side is ready to receive (rz)
		// If we have upload data, send ZFILE
		if s.State == zmInit && len(s.UploadData) > 0 {
			s.State = zmFile
			if s.File == nil {
				s.File = &ZFileInfo{Name: "upload.dat", Size: int64(len(s.UploadData))}
			}
			responses = append(responses, buildZfileFrame(s.File.Name, s.File.Size))
		}

	case ZRPOS:
		// Remote side wants resume from position
		if s.State == zmFile && len(s.UploadData) > 0 {
			s.State = zmData
			s.UploadOffset = 0
			// Send first ZDATA chunk
			chunk := s.nextUploadChunk()
			if chunk != nil {
				responses = append(responses, chunk)
			}
		}

	case ZACK:
		// Remote side acknowledged a ZDATA frame
		// Send next chunk if more data
		if s.State == zmData && len(s.UploadData) > 0 {
			chunk := s.nextUploadChunk()
			if chunk != nil {
				responses = append(responses, chunk)
			} else {
				// All data sent, send ZEOF
				s.State = zmEnd
				responses = append(responses, buildZeofFrame())
			}
		}

	case ZSKIP, ZNAK, ZABORT:
		// Error or skip — abort
		s.State = zmIdle
	}

	return responses
}

// nextUploadChunk returns the next ZDATA chunk for upload, or nil if done.
// The first call includes the binary header (**\x18A ZDATA flags CRC16) before
// the subpacket; subsequent calls return only the subpacket.
func (s *ZmSession) nextUploadChunk() []byte {
	if s.UploadOffset >= len(s.UploadData) {
		return nil
	}
	end := s.UploadOffset + s.UploadChunkSz
	if end > len(s.UploadData) {
		end = len(s.UploadData)
	}
	chunk := s.UploadData[s.UploadOffset:end]
	s.UploadOffset = end

	// Determine subpacket type
	subType := byte(ZCRCG)
	if s.UploadOffset >= len(s.UploadData) {
		subType = ZCRCE // last chunk
	}

	subpacket := buildZdataPacket(subType, chunk)

	// Include binary header for the first chunk so the remote ZMODEM
	// implementation knows this is a ZDATA frame.
	if s.UploadFirstChunk {
		s.UploadFirstChunk = false
		var flags [4]byte
		header := buildBin16Header(ZDATA, flags)
		// CRC-16 over type+flags
		crc := crc16Xmodem(header[3:])
		crcBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(crcBytes, crc)
		return append(append(header, crcBytes...), subpacket...)
	}

	return subpacket
}

// ---- Helpers ----

// parseZFileInfo extracts file metadata from a ZFILE payload.
// Format: name\0 size\0 mode\0 serial\0 mtime\0 [remaining...]
func parseZFileInfo(data []byte) *ZFileInfo {
	info := &ZFileInfo{Name: "download.dat"}
	parts := splitNull(data)
	if len(parts) >= 1 && parts[0] != "" {
		info.Name = parts[0]
	}
	if len(parts) >= 2 {
		info.Size, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	if len(parts) >= 3 {
		mode, _ := strconv.ParseUint(parts[2], 8, 32)
		info.Mode = uint32(mode)
	}
	if len(parts) >= 4 {
		info.Serial, _ = strconv.Atoi(parts[3])
	}
	if len(parts) >= 5 {
		info.Mtime, _ = strconv.ParseInt(parts[4], 10, 64)
	}
	return info
}

// splitNull splits data by null bytes.
func splitNull(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	// Trim trailing nulls
	s = strings.TrimRight(s, "\x00")
	return strings.Split(s, "\x00")
}

// extractZdataPayload extracts file data from ZDATA subpackets.
// Each subpacket: ZDLE, type, data..., CRC16(2)
func extractZdataPayload(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	var payload bytes.Buffer
	pos := 0
	for pos < len(data) {
		// Find ZDLE
		zdleIdx := bytes.IndexByte(data[pos:], ZDLE)
		if zdleIdx < 0 {
			break
		}
		pos += zdleIdx
		if pos+1 >= len(data) {
			break
		}
		subType := data[pos+1]
		// Check if it's a valid subpacket type
		if subType != ZCRCE && subType != ZCRCG && subType != ZCRCQ && subType != ZCRCW {
			pos++
			continue
		}
		// Data starts after the type byte and ends 2 bytes before the end
		// (the last 2 bytes are CRC-16)
		dataStart := pos + 2
		if dataStart >= len(data)-2 {
			break
		}
		// Find the next ZDLE to determine the end of this subpacket
		nextZdle := bytes.IndexByte(data[dataStart:], ZDLE)
		if nextZdle < 0 {
			// This is the last subpacket; data extends to end-2 (CRC)
			if len(data)-2 > dataStart {
				payload.Write(data[dataStart : len(data)-2])
			}
			break
		}
		// Data extends from dataStart to next ZDLE position
		// But we need to subtract 2 for CRC
		subEnd := dataStart + nextZdle
		if subEnd-2 > dataStart {
			payload.Write(data[dataStart : subEnd-2])
		}
		pos = dataStart + nextZdle
	}
	return payload.Bytes()
}

// ---- Streaming ZMODEM detector ----

// ZmDetector watches a byte stream for ZMODEM headers and switches to
// ZMODEM mode when detected.
type ZmDetector struct {
	buf      bytes.Buffer
	detected bool
	headerAt int // position in buf where header starts
}

// NewZmDetector creates a new ZMODEM detector.
func NewZmDetector() *ZmDetector {
	return &ZmDetector{}
}

// Write adds data to the detector. Returns true if a ZMODEM header was detected.
func (d *ZmDetector) Write(data []byte) bool {
	if d.detected {
		return true
	}
	d.buf.Write(data)
	idx := IndexZmodemHeader(d.buf.Bytes())
	if idx >= 0 {
		d.detected = true
		d.headerAt = idx
		return true
	}
	// Keep buffer from growing too large (keep last 16 bytes for header detection)
	if d.buf.Len() > 64 {
		excess := d.buf.Bytes()[:d.buf.Len()-16]
		d.buf.Next(len(excess))
	}
	return false
}

// FlushBefore returns all data before the ZMODEM header, and clears the buffer.
func (d *ZmDetector) FlushBefore() []byte {
	if !d.detected || d.headerAt <= 0 {
		data := d.buf.Bytes()
		d.buf.Reset()
		return data
	}
	prefix := make([]byte, d.headerAt)
	copy(prefix, d.buf.Bytes()[:d.headerAt])
	// Remove prefix from buffer
	d.buf.Next(d.headerAt)
	d.headerAt = 0
	return prefix
}

// Remaining returns all data in the buffer (including the header).
func (d *ZmDetector) Remaining() []byte {
	return d.buf.Bytes()
}

// Reset clears the detector.
func (d *ZmDetector) Reset() {
	d.buf.Reset()
	d.detected = false
	d.headerAt = 0
}

// HasData returns true if there's data in the buffer.
func (d *ZmDetector) HasData() bool {
	return d.buf.Len() > 0
}

// ---- ZMODEM stream reader ----

// ZmReader wraps an io.Reader and intercepts ZMODEM data.
// Data before the ZMODEM header is passed through normally.
// After the header is detected, all data is captured for ZMODEM processing.
type ZmReader struct {
	inner    io.Reader
	detector *ZmDetector
	captured bool // in ZMODEM capture mode
}

// NewZmReader creates a new ZMODEM reader.
func NewZmReader(r io.Reader) *ZmReader {
	return &ZmReader{inner: r, detector: NewZmDetector()}
}

// Read reads from the underlying reader. If ZMODEM is detected, the data
// is captured and not returned to the caller. The caller should check
// IsCapturing() and use GetCapturedData() to get the ZMODEM data.
func (r *ZmReader) Read(p []byte) (int, error) {
	if r.captured {
		// In capture mode: read everything into the detector
		n, err := r.inner.Read(p)
		if n > 0 {
			r.detector.Write(p[:n])
		}
		return n, err
	}

	n, err := r.inner.Read(p)
	if n <= 0 {
		return n, err
	}

	if r.detector.Write(p[:n]) {
		// ZMODEM detected! Flush the data before the header.
		prefix := r.detector.FlushBefore()
		if len(prefix) > 0 {
			copyLen := copy(p, prefix)
			r.captured = true
			return copyLen, nil
		}
		// No prefix data — header starts at beginning
		r.captured = true
		return 0, nil
	}

	// No ZMODEM detected yet — pass through
	return n, err
}

// IsCapturing returns true if ZMODEM data is being captured.
func (r *ZmReader) IsCapturing() bool {
	return r.captured
}

// GetCapturedData returns all captured ZMODEM data and resets the detector.
func (r *ZmReader) GetCapturedData() []byte {
	data := r.detector.Remaining()
	r.detector.Reset()
	return data
}

// ======== ZMODEM file transfer ========

// ZmDownload handles a ZMODEM download (sz) session.
// It reads ZMODEM frames from the PTY output reader, extracts the file data,
// and sends it to the caller via the data callback.
// Returns the file name, data, and error.
func ZmDownload(reader io.Reader, writer io.Writer) (name string, data []byte, err error) {
	session := NewZmSession()
	session.State = zmInit

	buf := make([]byte, 32<<10) // 32KB read buffer
	accum := &bytes.Buffer{}

	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			accum.Write(buf[:n])
		}

		// Try to parse frames from accumulated data
		for accum.Len() > 0 {
			data := accum.Bytes()
			frame, consumed, parseErr := parseZmFrame(data)
			if parseErr != nil {
				// Invalid frame — skip one byte and retry
				accum.Next(1)
				continue
			}
			if frame == nil {
				// Not enough data for a complete frame
				break
			}

			// Consume the frame
			accum.Next(consumed)

			// Process the frame
			responses := session.HandleFrame(frame)
			for _, resp := range responses {
				if _, werr := writer.Write(resp); werr != nil {
					return "", nil, fmt.Errorf("write response: %w", werr)
				}
			}

			// Check if download is complete
			if session.State == zmIdle && session.DataBuf.Len() > 0 {
				fname := "download.dat"
				if session.File != nil && session.File.Name != "" {
					fname = session.File.Name
				}
				return fname, session.DataBuf.Bytes(), nil
			}
		}

		if readErr != nil {
			// If we have accumulated data on error, return what we have
			if session.DataBuf.Len() > 0 {
				fname := "download.dat"
				if session.File != nil && session.File.Name != "" {
					fname = session.File.Name
				}
				return fname, session.DataBuf.Bytes(), nil
			}
			return "", nil, readErr
		}

		// Prevent infinite accumulation
		if accum.Len() > 1<<20 { // 1MB max buffer
			return "", nil, fmt.Errorf("zmodem download buffer overflow")
		}
	}
}

// ZmUpload handles a ZMODEM upload (rz) session.
// It sends the file data to the PTY stdin using the ZMODEM protocol.
// reader is the PTY output (to receive ACKs), writer is the PTY stdin.
func ZmUpload(reader io.Reader, writer io.Writer, filename string, fileData []byte) error {
	session := NewZmSession()
	session.State = zmInit
	session.File = &ZFileInfo{Name: filename, Size: int64(len(fileData))}
	session.UploadData = fileData
	session.UploadOffset = 0

	buf := make([]byte, 32<<10)
	accum := &bytes.Buffer{}

	// Send ZRQINIT to start the session (if we're the initiator)
	// Actually, for rz, the remote side sends ZRQINIT first. We respond.
	// But we need to wait for the remote side to send ZRQINIT before responding.

	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			accum.Write(buf[:n])
		}

		// Try to parse frames
		for accum.Len() > 0 {
			data := accum.Bytes()
			frame, consumed, parseErr := parseZmFrame(data)
			if parseErr != nil {
				accum.Next(1)
				continue
			}
			if frame == nil {
				break
			}
			accum.Next(consumed)

			responses := session.HandleFrame(frame)
			for _, resp := range responses {
				if _, werr := writer.Write(resp); werr != nil {
					return fmt.Errorf("write response: %w", werr)
				}
			}

			// Check if upload is complete
			if session.State == zmIdle {
				return nil
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}

		if accum.Len() > 1<<20 {
			return fmt.Errorf("zmodem upload buffer overflow")
		}
	}
}
