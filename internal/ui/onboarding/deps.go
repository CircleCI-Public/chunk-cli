package onboarding

import (
	"context"
	"errors"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Setup reads and drives the project's Setup run in the daemon.
type Setup interface {
	Start(ctx context.Context, orgID string) (chunkd.SetupActivity, error)
	Fetch(ctx context.Context, after int) (act chunkd.SetupActivity, found bool, err error)
	Continue(ctx context.Context) error
}

// Account is the CircleCI sign-in and org choice.
type Account interface {
	// Org is the project's org: the resolved one, or the only one; orgs lists
	// the choices when there are several. ErrSignedOut when there is no token.
	Org(ctx context.Context) (org string, orgs []Org, err error)
}

// Org is a CircleCI organization the developer can set the project up in.
type Org struct{ ID, Name string }

// ErrSignedOut is Account.Org's error when there is no CircleCI token.
var ErrSignedOut = errors.New("not signed in to CircleCI")

// Opened is a plan opened by an Opener: its agent, approver and store, and
// Root, the project the plan is planned and built in. Root is "" when there
// is no known checkout, and then the plan is read-only: its agent's Resume
// and its approver refuse, saying why.
type Opened struct {
	Agent   plan.Agent
	Approve plan.Approver
	Store   plan.Store
	Root    string
}

// Opener opens plan id, or a new plan when id is "", preferring root, as
// watch's PlanOpener does.
type Opener func(ctx context.Context, root, id string) (Opened, error)

// PRChecks reads a PR's checks; nil, nil when it can't be seen.
type PRChecks func(ctx context.Context, prURL string) (*Checks, error)

// Checks is a PR's checks: their overall state and how many are in each.
type Checks struct {
	State                   string
	Passed, Failed, Pending int
}

// Browser shows url to the developer; an error means it could not, and the
// card shows the URL instead.
type Browser func(url string) error

// Deps are what onboarding reads and drives, which its host wires.
type Deps struct {
	Root    string
	Setup   Setup
	Account Account
	Open    Opener
	Checks  PRChecks
	Browser Browser
	State   StateStore
}
