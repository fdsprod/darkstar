import { test, expect } from '@playwright/test';
import { installEmptyControlPlane } from './acceptance.fixtures';

async function board(page: import('@playwright/test').Page) {
 await installEmptyControlPlane(page);
 const now='2026-09-08T12:00:00Z';
 const project={id:'project_1',name:'Factory',status:'active',resourceVersion:1,lastGlobalPosition:1,createdAt:now,updatedAt:now};
 let work:any={id:'work_1',projectId:project.id,title:'Review the delivery',details:'Keep history',evidence:[],routingIntent:{mode:'automatic'},priority:2,status:'active',resourceVersion:1,lastGlobalPosition:2,createdAt:now,updatedAt:now};
 const run={id:'run_1',workItemId:work.id,workflowId:'delivery',workflowVersion:'1.0.0',status:'running',resourceVersion:1,lastGlobalPosition:3,createdAt:now,updatedAt:now};
 let moves=0, deletes=0;
 const columns=['backlog','ready','running','waiting','blocked','review','failed','done'];
 const plan=()=>({schemaVersion:1,workItemId:work.id,resourceVersion:3,state:'running',targets:columns.map(target=>({target,availability:!work.deletion && target==='waiting'?'enabled':'disabled',disabledReasons:target==='waiting'&&!work.deletion?[]:['unsupported_target'],confirmation:'none'}))});
 await page.route('**/api/v1/projects',r=>r.fulfill({json:[project]}));
 await page.route('**/api/v1/work-items?**',r=>r.fulfill({json:[work]}));
 await page.route('**/api/v1/work-items',r=>r.fulfill({json:work.deletion==='deleted'?[]:[work]}));
 await page.route('**/api/v1/runs?**',r=>r.fulfill({json:{items:[run],pageInfo:{nextCursor:null}}}));
 await page.route('**/api/v1/runs',r=>r.fulfill({json:{items:[run],pageInfo:{nextCursor:null}}}));
 await page.route('**/api/v1/work-items/work_1/transition-plan**',r=>r.fulfill({json:plan()}));
 await page.route('**/api/v1/work-items/work_1/transitions',r=>{moves++;return r.fulfill({status:412,json:{schemaVersion:1,code:'WORK_TRANSITION_VERSION_CONFLICT',message:'Changed',workTransitionPlan:plan()}})});
 await page.route('**/api/v1/work-items/work_1',r=>{if(r.request().method()==='DELETE'){deletes++;expect(r.request().headers()['if-match']).toBe('"1"');work={...work,deletion:'deleted',resourceVersion:2};return r.fulfill({status:202,json:work})}return r.fulfill({json:{work,runs:[run],stories:[],points:[]}})});
 await page.goto('/board');
 const card=page.locator('.work-card').filter({hasText:work.title});await expect(card).toBeVisible();
 return {card,title:work.title,moves:()=>moves,deletes:()=>deletes,setDeleting:()=>{work={...work,deletion:'deleting'}}};
}

test('cards are always draggable, blocked drops send no command, stale allowed drops stay put',async({page})=>{
 await page.setViewportSize({width:1600,height:950});const state=await board(page);
 await expect(state.card).toHaveAttribute('draggable','true');
 await expect(state.card).not.toContainText('work_1');
 await expect(state.card.getByText('Quick view',{exact:true})).toHaveCount(0);
 await expect(state.card.locator('.move-menu')).toHaveCount(0);
 await state.card.dragTo(page.locator('[data-lifecycle="blocked"]'));expect(state.moves()).toBe(0);
 await state.card.dragTo(page.locator('[data-lifecycle="waiting"]'));await expect.poll(state.moves).toBe(1);
 await expect(page.locator('[data-lifecycle="running"] .work-card')).toBeVisible();
 await expect(page.getByText('This item changed before the action completed. The board was refreshed; try again.')).toBeVisible();
 await state.card.focus();await page.keyboard.press('Space');await page.keyboard.press('ArrowRight');await page.keyboard.press('Enter');await expect.poll(state.moves).toBe(2);
 state.setDeleting();await page.reload();await expect(state.card).toHaveAttribute('draggable','true');
 await state.card.dragTo(page.locator('[data-lifecycle="waiting"]'));expect(state.moves()).toBe(2);
});

test('delete stops active work through the API and retained history is discoverable',async({page})=>{
 const state=await board(page);page.on('dialog',d=>d.accept());
 await state.card.getByRole('button',{name:'Delete work item',exact:true}).click();
 await expect.poll(state.deletes).toBe(1);await expect(state.card).toHaveCount(0);
 await page.getByRole('checkbox',{name:'Show deleted'}).check();await expect(state.card).toBeVisible();
 await expect(state.card).toContainText('Deleted · history retained');
 await state.card.getByRole('button',{name:state.title,exact:true}).click();await expect(page.getByRole('complementary',{name:state.title})).toBeVisible();
});

test('board uses all available width and final columns remain reachable',async({page},testInfo)=>{
 await page.setViewportSize({width:2270,height:1440});const state=await board(page);
 const layout=page.locator('.board-operational-layout');const grid=page.getByRole('region',{name:'Work lifecycle'});
 const outer=await layout.boundingBox(),inner=await grid.boundingBox();expect(Math.abs(outer!.width-inner!.width)).toBeLessThan(2);
 await page.screenshot({path:testInfo.outputPath('board-wide.png')});
 await state.card.getByRole('button',{name:state.title,exact:true}).click();await expect(layout).toHaveClass(/--detail/);
 await page.getByRole('button',{name:'Close quick view'}).click();await expect(layout).not.toHaveClass(/--detail/);
 await page.setViewportSize({width:900,height:750});
 await grid.evaluate(el=>{el.scrollLeft=el.scrollWidth});
 const done=await page.locator('[data-lifecycle="done"]').boundingBox(),viewport=await grid.boundingBox();
 expect(done!.x+done!.width).toBeLessThanOrEqual(viewport!.x+viewport!.width+1);
 await page.screenshot({path:testInfo.outputPath('board-narrow-scrolled.png')});
});

test('dragging a focused title keeps the pointer drag active after focus leaves the card', async ({page}) => {
 const state = await board(page);
 const title = state.card.getByRole('button', {name: state.title, exact: true});
 await title.focus();
 const transfer = await page.evaluateHandle(() => new DataTransfer());
 await title.dispatchEvent('dragstart', {dataTransfer: transfer});
 await title.dispatchEvent('focusout', {relatedTarget: null});
 await expect(page.locator('[data-lifecycle="waiting"]')).toHaveAttribute('data-drop-available','true');
 await page.locator('[data-lifecycle="waiting"]').dispatchEvent('drop', {dataTransfer: transfer});
 await expect.poll(state.moves).toBe(1);
});
