package workflowchat

import (
 "encoding/json"
 "strings"
 "testing"
 "darkstar/src/core/workflow"
)
func TestWorkspaceExampleAndRequiredConnections(t *testing.T){
 for _,tc:=range []struct{name,old,replacement string;valid bool}{
  {name:"valid current checkout",valid:true},
  {"valid worktree",`"mode":"current_checkout"`,`"mode":"new_worktree","baseRef":"HEAD","branch":"darkstar/{runId}"`,true},
  {"missing workspace",`"workspaceInput":"workspace"`,`"workspaceInput":"missing"`,false},
  {"wrong repository",`"repositoryInput":"repository"`,`"repositoryInput":"task"`,false},
  {"missing checks",`"checks":[["git","diff","--check"]]`,`"checks":[]`,false},
 }{t.Run(tc.name,func(t *testing.T){raw:=workspaceExample;if tc.old!=""{raw=strings.Replace(raw,tc.old,tc.replacement,1)};doc,err:=workflow.Decode([]byte(raw));if err!=nil{t.Fatal(err)};issues:=workflow.Validate(doc);if (len(issues)==0)!=tc.valid{t.Fatalf("validation: %v",issues)};encoded,err:=json.Marshal(doc);if err!=nil{t.Fatal(err)};if _,err=workflow.Decode(encoded);err!=nil{t.Fatal(err)}})}
 for _,raw:=range []string{strings.Replace(workspaceExample,`"mode":"current_checkout"`,`"mode":"current_checkout","baseRef":"main"`,1),strings.Replace(workspaceExample,`"mode":"current_checkout"`,`"mode":"new_worktree"`,1)}{if _,err:=workflow.Decode([]byte(raw));err==nil{t.Fatal("contradictory or incomplete checkout accepted")}}
}
