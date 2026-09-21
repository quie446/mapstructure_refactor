package mapstructure

import (
	stderrors "errors"
	"fmt"
	"sort"
	"strings"
)

// AggregateLock is the queryable gate over a set of binding issues. It only
// releases when ordering, per-item inspectability and migration guidance all
// hold, and it invalidates itself as soon as the pinned policy changes.
type AggregateLock struct {
	policySignature string

	issues       []BindingIssue
	migration    map[IssueCategory]string
	locked       bool
	lockEvidence string
}

// NewAggregateLock prepares a lock for a binding report and the policy that
// produced it. Nothing is released until Lock passes.
func NewAggregateLock(report *BindingReport, policy BindingPolicy) *AggregateLock {
	issues := []BindingIssue{}
	if report != nil {
		issues = append(issues, report.Issues...)
	}

	sortIssues(issues)

	return &AggregateLock{
		policySignature: policy.Signature(),
		issues:          dedupeIssues(issues),
		migration:       cloneMigration(policy.MigrationNotes),
	}
}

// Issues returns a copy of the ordered, individually inspectable issue set.
func (l *AggregateLock) Issues() []BindingIssue {
	return append([]BindingIssue(nil), l.issues...)
}

// Lock validates every gate and returns an error naming the concrete failure.
// Extra and missing fields occurring together are treated as a breaking
// aggregate that requires migration notes before it can be released.
func (l *AggregateLock) Lock() error {
	if l.locked {
		return stderrors.New("aggregate lock: already locked; re-create the lock to change inputs")
	}

	if len(l.issues) == 0 {
		l.locked = true
		l.lockEvidence = l.evidence()

		return nil
	}

	if err := l.validateOrdering(); err != nil {
		return err
	}

	if err := l.validateInspectability(); err != nil {
		return err
	}

	if err := l.validateMigration(); err != nil {
		return err
	}

	l.locked = true
	l.lockEvidence = l.evidence()

	return nil
}

// Locked reports whether the gate currently holds.
func (l *AggregateLock) Locked() bool { return l.locked }

// Verify re-runs the gate against a current policy signature. A strategy change
// after locking forces the whole set through the gate again.
func (l *AggregateLock) Verify(policy BindingPolicy) error {
	if !l.locked {
		return stderrors.New("aggregate lock: cannot verify before Lock succeeds")
	}

	if policy.Signature() != l.policySignature {
		l.locked = false

		return fmt.Errorf(
			"aggregate lock: error strategy changed after locking (expected signature %s, got %s); the issue set must pass the gate again",
			l.policySignature,
			policy.Signature(),
		)
	}

	if l.evidence() != l.lockEvidence {
		l.locked = false

		return stderrors.New("aggregate lock: issue set changed after locking; the issue set must pass the gate again")
	}

	return nil
}

func (l *AggregateLock) validateOrdering() error {
	for index := 1; index < len(l.issues); index++ {
		prev, cur := l.issues[index-1], l.issues[index]

		if prev.Category > cur.Category {
			return fmt.Errorf("aggregate lock: issue order is not deterministic at index %d (category %q after %q)",
				index, cur.Category, prev.Category)
		}

		if prev.Category == cur.Category && prev.Path > cur.Path {
			return fmt.Errorf("aggregate lock: issue order is not deterministic at index %d (path %q after %q)",
				index, cur.Path, prev.Path)
		}
	}

	return nil
}

func (l *AggregateLock) validateInspectability() error {
	seen := map[BindingIssue]struct{}{}

	for index, issue := range l.issues {
		if issue.Category == "" {
			return fmt.Errorf("aggregate lock: issue at index %d has no category and cannot be checked individually", index)
		}

		if strings.TrimSpace(issue.Message) == "" {
			return fmt.Errorf("aggregate lock: issue %q has no failure reason; an unreported failure blocks release", issue.Path)
		}

		if _, duplicate := seen[issue]; duplicate {
			return fmt.Errorf("aggregate lock: duplicate issue %q prevents per-item checking", issue)
		}

		seen[issue] = struct{}{}
	}

	return nil
}

func (l *AggregateLock) validateMigration() error {
	categories := map[IssueCategory]struct{}{}

	for _, issue := range l.issues {
		categories[issue.Category] = struct{}{}
	}

	// Simultaneous extra and missing fields are the breaking combination that
	// demands explicit migration guidance on both sides.
	_, hasUnused := categories[CategoryUnusedKey]
	_, hasMissing := categories[CategoryMissingRequired]

	if !hasUnused && !hasMissing {
		return nil
	}

	missingNotes := []string{}

	if hasMissing {
		if strings.TrimSpace(l.migration[CategoryMissingRequired]) == "" {
			missingNotes = append(missingNotes, string(CategoryMissingRequired))
		}
	}

	if hasUnused {
		if strings.TrimSpace(l.migration[CategoryUnusedKey]) == "" {
			missingNotes = append(missingNotes, string(CategoryUnusedKey))
		}
	}

	if len(missingNotes) > 0 {
		sort.Strings(missingNotes)

		return fmt.Errorf(
			"aggregate lock: breaking aggregate with extra and missing fields lacks migration notes for: %s",
			strings.Join(missingNotes, ", "),
		)
	}

	return nil
}

func (l *AggregateLock) evidence() string {
	parts := make([]string, 0, len(l.issues))
	for _, issue := range l.issues {
		parts = append(parts, issue.String())
	}

	return stableFingerprint(strings.Join(parts, "\n"))
}

func cloneMigration(notes map[IssueCategory]string) map[IssueCategory]string {
	out := make(map[IssueCategory]string, len(notes))
	for category, note := range notes {
		out[category] = note
	}

	return out
}
