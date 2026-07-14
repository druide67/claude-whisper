package peerid

import "testing"

func TestValid(t *testing.T) {
	good := []string{"frontend", "claude-backend", "a", "A1", "remote-agent", "x9-y8"}
	for _, s := range good {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	bad := []string{"", "-foo", "--transport", "-h", "foo bar", "foo/bar", "foo.bar", "café"}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestValidSession(t *testing.T) {
	if !ValidSession("abcd1234") { // 8 chars, boundary
		t.Error("8-char session id should be valid")
	}
	if ValidSession("short7x") { // 7 chars
		t.Error("7-char session id should be invalid")
	}
	if ValidSession("../evil") {
		t.Error("traversal session id must be rejected")
	}
	if ValidSession("") {
		t.Error("empty session id must be rejected")
	}
}

func TestValidMsgID(t *testing.T) {
	if !ValidMsgID("msg-1779996645-be50630d") {
		t.Error("canonical msg id should be valid")
	}
	for _, s := range []string{"msg-9-zz", "msg-", "msg-abc-def", "notmsg-1-a", "../msg-1-a"} {
		if ValidMsgID(s) {
			t.Errorf("ValidMsgID(%q) = true, want false", s)
		}
	}
}

func TestValidLengthBound(t *testing.T) {
	long := "a"
	for len(long) < 64 {
		long += "b"
	}
	if !Valid(long) {
		t.Error("64-char peer-id should be valid")
	}
	if Valid(long + "b") {
		t.Error("65-char peer-id must be rejected (path/sentinel key abuse)")
	}
}
