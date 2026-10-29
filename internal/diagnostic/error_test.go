package diagnostic

import (
	"errors"
	"fmt"
	"testing"
)

func TestMessageAndHintKeepUnderlyingErrorIdentity(t *testing.T) {
	cause := errors.New("low-level details")
	err := fmt.Errorf("setup failed: %w", New("friendly explanation", "try this instead", cause))
	if err.Error() != "setup failed: friendly explanation" || Hint(err) != "try this instead" || !errors.Is(err, cause) {
		t.Fatal(err, Hint(err))
	}
	err = New("outer context", "", err)
	if Hint(err) != "try this instead" {
		t.Fatal("lost nested hint")
	}
}
