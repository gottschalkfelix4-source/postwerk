package smtp

import (
	"bufio"
	"bytes"
	"errors"
)

// dataResult describes why a message read in the DATA phase was rejected.
type dataResult int

const (
	dataOK dataResult = iota
	dataTooBig
	dataBareNewline
	dataLineTooLong
)

var errLineTooLong = errors.New("line too long")

// readData reads a dot-stuffed message body up to the end-of-data marker.
//
// Only the exact sequence CRLF "." CRLF terminates the data, where the leading
// CRLF may be the one ending the DATA command itself. Lines ending in a bare LF
// or containing a bare CR mark the message as invalid but never end it, which
// prevents SMTP smuggling via ambiguous terminators. Lines are delimited by LF
// only, so a fragment returned because the read buffer was full is never
// treated as the start of a new line.
//
// Memory use is bounded: at most maxSize bytes of message plus one line of at
// most maxLine bytes are held. Once a limit is exceeded the rest of the
// message is drained and discarded. The returned data is nil unless the
// result is dataOK.
func readData(r *bufio.Reader, maxSize int64, maxLine int) ([]byte, dataResult, error) {
	var (
		buf       []byte
		line      []byte
		overflow  bool // current line exceeded maxLine, rest of it is discarded
		prevCR    bool // previous fragment of the current line ended in CR
		lineStart = true
		tooBig    bool
		bareNL    bool
		longLine  bool
	)
	for {
		chunk, err := r.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull {
			return nil, dataOK, err
		}
		if !overflow {
			if len(line)+len(chunk) > maxLine+2 {
				overflow = true
				longLine = true
				line = line[:0]
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			if len(chunk) > 0 {
				prevCR = chunk[len(chunk)-1] == '\r'
			}
			continue
		}

		// chunk ends in LF: the line is complete.
		var endsCRLF bool
		if len(chunk) >= 2 {
			endsCRLF = chunk[len(chunk)-2] == '\r'
		} else {
			endsCRLF = prevCR
		}
		prevCR = false

		if !overflow {
			content := line[:len(line)-1]
			if endsCRLF {
				content = content[:len(content)-1]
			}
			if lineStart && endsCRLF && len(content) == 1 && content[0] == '.' {
				break
			}
			if !endsCRLF || bytes.IndexByte(content, '\r') >= 0 {
				bareNL = true
			}
			if lineStart && len(content) > 0 && content[0] == '.' {
				content = content[1:]
			}
			if !tooBig && !bareNL && !longLine {
				buf = append(buf, content...)
				buf = append(buf, '\r', '\n')
				if int64(len(buf)) > maxSize {
					tooBig = true
				}
			}
		}
		if tooBig || bareNL || longLine {
			buf = nil
		}
		overflow = false
		line = line[:0]
		lineStart = endsCRLF
	}
	switch {
	case bareNL:
		return nil, dataBareNewline, nil
	case longLine:
		return nil, dataLineTooLong, nil
	case tooBig:
		return nil, dataTooBig, nil
	}
	if buf == nil {
		buf = []byte{}
	}
	return buf, dataOK, nil
}

// readLimitedLine reads one line terminated by LF and returns it without the
// line ending. If the line (including its ending) exceeds max bytes, it stops
// reading and returns errLineTooLong without buffering the remainder.
func readLimitedLine(r *bufio.Reader, max int) (string, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > max {
			return "", errLineTooLong
		}
		if err == bufio.ErrBufferFull {
			line = append(line, chunk...)
			continue
		}
		if err != nil {
			return "", err
		}
		line = append(line, chunk...)
		break
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return string(line), nil
}
