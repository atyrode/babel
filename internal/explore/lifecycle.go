package explore

import (
 "context"
 "fmt"
 "os"

 "github.com/atyrode/babel/internal/run"
)

// A stop file is a request, not a signal to a process tree. Workers already in
// flight finish; the next safe point records the interruption without launching.
func (c *Controller) safePoint(st *state, stage Stage) bool {
 stopped := st.ctx.Err() != nil
 if st.opt.StopFile != "" {
  _, err := os.Stat(st.opt.StopFile)
  if err == nil { stopped = true } else if !os.IsNotExist(err) {
   st.fail(stage, FailureStorage, c.now(), fmt.Errorf("explore: read stop file: %w", err))
   stopped = true
  }
 }
 if stopped {
  st.out.Cancelled = true
  cause := context.Cause(st.ctx)
  if cause == nil { cause = fmt.Errorf("operator stop file") }
  st.fail(stage, FailureCancelled, c.now(), fmt.Errorf("explore: interrupted at a safe point: %w", cause))
  return false
 }
 st.stage = stage
 if st.lifecycle == run.Running || st.lifecycle == run.Resumed {
  return c.writeReceipt(st, st.opt.RunID, nil, nil, nil, st.started) != nil
 }
 return true
}
