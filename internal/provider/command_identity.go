package provider

import (
	"fmt"

	"github.com/google/uuid"
)

// A replay uses the same observed revision, while a later lifecycle cycle is a
// new command even when its action and human reason are unchanged.
func reasonedCommandKey(orgID, resourceID, action string, revision int64, reason string) string {
	return uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", orgID, resourceID, action, revision, reason))).String()
}
