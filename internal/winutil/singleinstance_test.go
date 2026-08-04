package winutil

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSingleInstanceMutex(t *testing.T) {
	name := fmt.Sprintf(`Local\PortPilot.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	first, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("first mutex acquisition reported an existing instance")
	}
	defer first.Close()

	second, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil || !alreadyRunning {
		t.Fatal("second mutex acquisition did not detect the existing instance")
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("mutex remained locked after close")
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
