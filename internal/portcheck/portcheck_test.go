package portcheck

import "testing"

func TestParseNetstatPID(t *testing.T) {
	output := "  TCP    0.0.0.0:8080       0.0.0.0:0       LISTENING       12345\r\n" +
		"  TCP    [::]:9000          [::]:0          LISTENING       42\r\n"
	pid, err := parseNetstatPID(output, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 12345 {
		t.Fatalf("got %d", pid)
	}
}
