package dbwire

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildBSONCommandDoc builds a minimal BSON document of the form
// {<cmdName>: "<argVal>"} -- enough to exercise bsonFirstKey / ParseMongoOpMsg.
func buildBSONCommandDoc(cmdName, argVal string) []byte {
	var elem bytes.Buffer
	elem.WriteByte(0x02) // BSON type: UTF-8 string
	elem.WriteString(cmdName)
	elem.WriteByte(0x00) // cstring terminator for element name

	var strLen [4]byte
	binary.LittleEndian.PutUint32(strLen[:], uint32(len(argVal)+1))
	elem.Write(strLen[:])
	elem.WriteString(argVal)
	elem.WriteByte(0x00) // string terminator

	var doc bytes.Buffer
	totalLen := 4 + elem.Len() + 1
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(totalLen))
	doc.Write(lenBuf[:])
	doc.Write(elem.Bytes())
	doc.WriteByte(0x00) // document terminator
	return doc.Bytes()
}

// buildMongoOpMsg builds a full OP_MSG wire message: header + flagBits +
// single body section (kind 0) containing doc.
func buildMongoOpMsg(requestID, responseTo int32, doc []byte) []byte {
	var body bytes.Buffer
	var flags [4]byte // flagBits = 0
	body.Write(flags[:])
	body.WriteByte(0x00) // section kind 0 = body
	body.Write(doc)

	total := 16 + body.Len()
	var out bytes.Buffer
	var msgLen, reqID, respTo, opCode [4]byte
	binary.LittleEndian.PutUint32(msgLen[:], uint32(total))
	binary.LittleEndian.PutUint32(reqID[:], uint32(requestID))
	binary.LittleEndian.PutUint32(respTo[:], uint32(responseTo))
	binary.LittleEndian.PutUint32(opCode[:], uint32(opMsg))
	out.Write(msgLen[:])
	out.Write(reqID[:])
	out.Write(respTo[:])
	out.Write(opCode[:])
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestParseMongoOpMsgFindCommand(t *testing.T) {
	doc := buildBSONCommandDoc("find", "users")
	frame := buildMongoOpMsg(1, 0, doc)

	q, err := ParseMongoOpMsg(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ParseMongoOpMsg: %v", err)
	}
	if q.Command != "find" {
		t.Fatalf("expected command 'find', got %q", q.Command)
	}
}

func TestParseMongoOpMsgInsertCommand(t *testing.T) {
	doc := buildBSONCommandDoc("insert", "logs")
	frame := buildMongoOpMsg(2, 0, doc)

	q, err := ParseMongoOpMsg(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ParseMongoOpMsg: %v", err)
	}
	if q.Command != "insert" {
		t.Fatalf("expected command 'insert', got %q", q.Command)
	}
}

func TestParseMongoOpMsgWrongOpcode(t *testing.T) {
	var out bytes.Buffer
	var msgLen, reqID, respTo, opCode [4]byte
	binary.LittleEndian.PutUint32(msgLen[:], 16)
	binary.LittleEndian.PutUint32(opCode[:], 1) // OP_REPLY, unsupported
	out.Write(msgLen[:])
	out.Write(reqID[:])
	out.Write(respTo[:])
	out.Write(opCode[:])

	_, err := ParseMongoOpMsg(bytes.NewReader(out.Bytes()))
	if err == nil {
		t.Fatalf("expected error for unsupported opcode")
	}
}

func TestMongoParserProtoParserRequestResponse(t *testing.T) {
	var mp MongoParser
	doc := buildBSONCommandDoc("ping", "1")
	reqFrame := buildMongoOpMsg(10, 0, doc) // responseTo=0 -> request

	op, ok := mp.ParseRequest(reqFrame)
	if !ok || op != "ping" {
		t.Fatalf("expected request 'ping', got %q ok=%v", op, ok)
	}
	if mp.ParseResponse(reqFrame) {
		t.Fatalf("did not expect request frame classified as response")
	}

	respDoc := buildBSONCommandDoc("ok", "1")
	respFrame := buildMongoOpMsg(11, 10, respDoc) // responseTo=10 -> response to request 10

	if mp.ParseResponse(reqFrame) {
		t.Fatalf("request frame should not be a response")
	}
	if !mp.ParseResponse(respFrame) {
		t.Fatalf("expected response frame recognized")
	}
	if _, ok := mp.ParseRequest(respFrame); ok {
		t.Fatalf("did not expect response frame classified as request")
	}
}

func TestMongoCorrelatorLatency(t *testing.T) {
	corr := NewCorrelator(MongoParser{})
	doc := buildBSONCommandDoc("find", "users")
	reqFrame := buildMongoOpMsg(5, 0, doc)
	respDoc := buildBSONCommandDoc("ok", "1")
	respFrame := buildMongoOpMsg(6, 5, respDoc)

	if _, ok := corr.Observe("conn-mongo", reqFrame, fixedTime(0)); ok {
		t.Fatalf("did not expect completion on request")
	}
	completed, ok := corr.Observe("conn-mongo", respFrame, fixedTime(15_000_000))
	if !ok {
		t.Fatalf("expected completion on response")
	}
	if completed.Op != "find" {
		t.Fatalf("expected op 'find', got %q", completed.Op)
	}
	if completed.Duration.Milliseconds() != 15 {
		t.Fatalf("expected 15ms latency, got %v", completed.Duration)
	}
}
