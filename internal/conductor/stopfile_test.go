package conductor_test

import (
 "os"
 "path/filepath"
 "testing"

 "github.com/atyrode/babel/internal/conductor"
 runstore "github.com/atyrode/babel/internal/run"
)

func TestStopFileHonorsTheCycleBoundary(t *testing.T) {
 path := filepath.Join(t.TempDir(),"stop")
 finished := false
 runner := &fakeRunner{hook:func(string,conductor.Assignment)(conductor.Result,error){
  if err := os.WriteFile(path,[]byte("stop"),0600); err != nil { return conductor.Result{},err }
  finished=true
  return conductor.Result{ReceiptID:"rcpt-stop"},nil
 }}
 loop, err := conductor.New(conductor.Config{
  Ceilings:testCeilings,Floor:conductor.Floor{OneIn:1},
  Ladder:[]conductor.Rung{&stubRung{name:conductor.RungSerendipity,work:&conductor.Assignment{Authority:runstore.Authority{Kind:runstore.AuthoritySerendipity,Ref:"draw:stop"}}}},
  Runner:runner,Ledger:fakeLedger{},Journal:testJournal(t),Now:(&clock{now:day}).Now,
 })
 if err != nil { t.Fatal(err) }
 if err := loop.Run(t.Context(),conductor.RunOptions{StopFile:path}); err != nil { t.Fatal(err) }
 if !finished || len(runner.runs)!=1 { t.Fatal("stop file did not finish exactly the active cycle") }
 if err := loop.Run(t.Context(),conductor.RunOptions{StopFile:path}); err != nil { t.Fatal(err) }
 if len(runner.runs)!=1 { t.Fatal("an existing stop file allowed another launch") }
}
