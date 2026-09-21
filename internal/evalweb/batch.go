package evalweb

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stevemurr/strap/eval"
)

// A batch is a directory whose children each hold one attempt at
// <task>/results: the layout the container launcher and page-launched jobs
// leave behind. The page treats a batch as one virtual ladder run, so it can
// be read and compared like a run that graded every task in one results dir.

// batchMembers lists a batch's member results directories in task order, or
// nothing when dir is not a batch.
func batchMembers(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var members []string
	for _, e := range entries {
		if !e.IsDir() || skippedDirs[e.Name()] {
			continue
		}
		results := filepath.Join(dir, e.Name(), "results")
		if _, err := os.Stat(filepath.Join(results, "results.jsonl")); err == nil {
			members = append(members, results)
		}
	}
	sort.Strings(members)
	return members
}

// addBatches appends one summary per batch found among the discovered runs.
// A group qualifies when every member sits at <group>/<attempt>/results.
func addBatches(root string, runs []RunSummary) []RunSummary {
	groups := map[string][]RunSummary{}
	var order []string
	for _, r := range runs {
		if r.Group == "" || r.Kind != Ladder {
			continue
		}
		if _, ok := groups[r.Group]; !ok {
			order = append(order, r.Group)
		}
		groups[r.Group] = append(groups[r.Group], r)
	}
	for _, group := range order {
		members := groups[group]
		nested := true
		for _, m := range members {
			parts := strings.Split(m.Path, "/")
			if len(parts) != 3 || parts[2] != "results" {
				nested = false
				break
			}
		}
		if !nested {
			continue
		}
		runs = append(runs, summarizeBatch(group, members))
	}
	return runs
}

func summarizeBatch(group string, members []RunSummary) RunSummary {
	s := RunSummary{Path: group, Name: group, Kind: Ladder, Batch: true, Members: len(members), Tiers: map[string]TierCount{}}
	for _, m := range members {
		if s.StartedAt.IsZero() || (!m.StartedAt.IsZero() && m.StartedAt.Before(s.StartedAt)) {
			s.StartedAt = m.StartedAt
		}
		if s.Model == "" {
			s.Model, s.Backend, s.Commit, s.Profile = m.Model, m.Backend, m.Commit, m.Profile
		}
		s.Tasks += m.Tasks
		s.Passed += m.Passed
		s.Failed += m.Failed
		s.BuildFailed += m.BuildFailed
		s.Errored += m.Errored
		s.Submitted += m.Submitted
		s.TimedOut += m.TimedOut
		s.NoReply += m.NoReply
		for tier, c := range m.Tiers {
			t := s.Tiers[tier]
			t.Tasks += c.Tasks
			t.Passed += c.Passed
			s.Tiers[tier] = t
		}
		if m.Error != "" && s.Error == "" {
			s.Error = m.Error
		}
	}
	return s
}

// batchSummary summarises one batch directory on demand.
func (s *Server) batchSummary(dir, rel string) RunSummary {
	var members []RunSummary
	for _, m := range batchMembers(dir) {
		memberRel, _ := filepath.Rel(s.root, m)
		members = append(members, summarize(m, filepath.ToSlash(memberRel)))
	}
	return summarizeBatch(filepath.ToSlash(filepath.Clean(rel)), members)
}

// batchDetail merges the members' analysed reports into one. Members are
// cached individually, so the merge itself is cheap.
func (s *Server) batchDetail(ctx context.Context, dir, rel string) (*ladderDetail, error) {
	detail := &ladderDetail{Summary: s.batchSummary(dir, rel), Results: map[string]eval.Result{}}
	detail.Report.Dir = dir
	for _, m := range batchMembers(dir) {
		memberRel, _ := filepath.Rel(s.root, m)
		member, err := s.ladder(ctx, filepath.ToSlash(memberRel))
		if err != nil {
			return nil, err
		}
		if detail.Report.Run.Model.Model == "" {
			detail.Report.Run = member.Report.Run
			detail.Report.Run.Tasks = nil
		}
		if detail.Report.Run.StartedAt.IsZero() || (!member.Report.Run.StartedAt.IsZero() && member.Report.Run.StartedAt.Before(detail.Report.Run.StartedAt)) {
			detail.Report.Run.StartedAt = member.Report.Run.StartedAt
		}
		detail.Report.Run.Tasks = append(detail.Report.Run.Tasks, member.Report.Run.Tasks...)
		detail.Report.Tasks = append(detail.Report.Tasks, member.Report.Tasks...)
		for id, r := range member.Results {
			detail.Results[id] = r
		}
	}
	tiers := map[string]int{}
	for i, tier := range eval.Tiers() {
		tiers[tier] = i
	}
	sort.SliceStable(detail.Report.Tasks, func(i, j int) bool {
		a, b := detail.Report.Tasks[i], detail.Report.Tasks[j]
		if tiers[a.Tier] != tiers[b.Tier] {
			return tiers[a.Tier] < tiers[b.Tier]
		}
		return a.TaskID < b.TaskID
	})
	detail.Report.Tiers = eval.Summarize(detail.Report.Tasks)
	return detail, nil
}

// batchTrace maps a task-relative trace path onto the member attempt that
// holds it: <task>/trace.jsonl lives at <batch>/<task>/results/<task>/trace.jsonl.
func batchTrace(dir, file string) string {
	task := strings.SplitN(filepath.ToSlash(file), "/", 2)[0]
	return filepath.Join(dir, task, "results", file)
}
