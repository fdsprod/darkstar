import {expect,test} from '@playwright/test';
import {installEmptyControlPlane} from './acceptance.fixtures';

test('worktree configuration requires a base ref and persists the explicit checkout plan',async({page},testInfo)=>{
 await installEmptyControlPlane(page);
 const document={apiVersion:'darkstar.local/v1alpha3',kind:'Workflow',metadata:{name:'workspace-example',version:'1.0.0'},spec:{inputs:{repository:{type:'repository',resource:{kind:'repository'}}},routeDefaults:{entry:'prepare',terminals:['prepare']},nodes:{prepare:{type:'workspace_prepare',displayName:'Prepare workspace',entry:true,terminal:true,inputs:{repository:{type:'repository',from:'run.input.repository'}},outputs:{workspace:{type:'workspace'}},workspacePrepare:{repositoryInput:'repository',checkout:{mode:'current_checkout'}},transitions:[]}}}};
 const draft={id:'workspace-draft',name:'workspace-example',scope:'user',scopeReference:'local-user',revision:1,document,layout:{},documentDigest:'b'.repeat(64),updatedAt:'2026-09-08T00:00:00Z'};
 await page.route('**/api/v1/workflows/library',route=>route.fulfill({json:{versions:[],drafts:[draft],archives:[]}}));
 let saved:any;
 await page.route('**/api/v1/workflows/drafts/**',route=>{if(route.request().url().endsWith('/update')){saved=route.request().postDataJSON();return route.fulfill({json:{...draft,...saved,revision:2}})}return route.fulfill({json:draft});});
 await page.goto('/workflows?item=draft%3Aworkspace-draft&view=canvas&selection=node%3Aprepare');
 await expect(page.getByLabel('Workspace mode')).toHaveValue('current_checkout');
 await page.getByLabel('Workspace mode').selectOption('new_worktree');
 await expect(page.getByRole('button',{name:'Apply node configuration',exact:true})).toBeDisabled();
 await page.getByLabel('Base ref',{exact:true}).fill('refs/heads/trunk');
 await page.getByLabel('New branch',{exact:true}).fill('darkstar/{runId}');
 await page.getByRole('button',{name:'Apply node configuration',exact:true}).click();
 await expect.poll(()=>saved?.document?.spec?.nodes?.prepare?.workspacePrepare?.checkout).toEqual({mode:'new_worktree',baseRef:'refs/heads/trunk',branch:'darkstar/{runId}'});
 await page.screenshot({path:testInfo.outputPath('workspace-configuration.png')});
});
