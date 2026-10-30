package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/run"
)

func TestRunStorePersistsOneExactPromptAcrossReconnect(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	fixture := seed(t, db, "run-prompt")
	service := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow })
	if _, err := service.Transition(context.Background(), fixture.run.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), fixture.run.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	prompt := "  Exact prompt bytes.\n\nKeep trailing spaces.  "
	sentAt := testNow.Add(time.Minute)
	stored, err := service.RecordPrompt(context.Background(), fixture.run.ID, prompt, sentAt)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(prompt))
	if stored.FinalCodexPrompt != prompt || stored.PromptSHA256 != hex.EncodeToString(sum[:]) || stored.PromptBytes != int64(len([]byte(prompt))) || stored.PromptSentAt == nil || !stored.PromptSentAt.Equal(sentAt) {
		t.Fatalf("stored prompt audit=%+v", stored)
	}
	if _, err := service.RecordPrompt(context.Background(), fixture.run.ID, prompt+"changed", sentAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable prompt error=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	reopened, err := NewRunStore(db).Get(context.Background(), fixture.run.ID)
	if err != nil || reopened.FinalCodexPrompt != prompt || reopened.PromptSHA256 != stored.PromptSHA256 || reopened.PromptSentAt == nil || !reopened.PromptSentAt.Equal(sentAt) {
		t.Fatalf("reopened prompt audit=%+v err=%v", reopened, err)
	}
}
