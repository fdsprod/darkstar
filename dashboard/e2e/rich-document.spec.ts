import {expect,test} from '@playwright/test';
test('rich document components render safely and anchor a formatted range across Unicode and CRLF',async({page})=>{
 await page.goto('/fixtures/rich-document.html');
 const reader=page.locator('.annotation-reader');
 await expect(reader.getByRole('heading',{name:'Component showcase'})).toBeVisible();
 await expect(reader.getByRole('heading',{name:'Endpoint'})).toHaveCount(1);
 await expect(reader.getByRole('checkbox')).toHaveCount(2);
 await expect(reader.getByText('Invalid datamodel',{exact:true})).toHaveCount(1);
 await expect(reader.getByRole('img',{name:'Mermaid diagram'})).toHaveCount(1);
 await expect(reader.locator('a[href^="javascript:"]')).toHaveCount(0);
 await expect(reader.locator('script')).toHaveCount(0);
 await reader.evaluate(element=>{const strong=element.querySelector('strong')!.querySelector('[data-source-start]')!.firstChild!,em=element.querySelector('em')!.querySelector('[data-source-start]')!.firstChild!;const range=document.createRange();range.setStart(strong,0);range.setEnd(em,em.textContent!.length);const selected=window.getSelection()!;selected.removeAllRanges();selected.addRange(range);element.dispatchEvent(new MouseEvent('mouseup',{bubbles:true}));});
 await expect(page.getByRole('dialog',{name:'Annotate selected text'})).toBeVisible();
 await page.screenshot({path:'out/annotation-popup.png',fullPage:true});
 await expect(page.getByLabel('Comment on selected text')).toBeFocused();
 await expect(page.getByLabel('Comment on selected text')).toBeFocused();
 await page.getByLabel('Comment on selected text').fill('Clarify both parts');
 await page.getByRole('button',{name:'+ Annotate',exact:true}).click();
 await expect(page.getByLabel("Saved anchors")).toContainText("Clarify both parts");
 const annotations=JSON.parse(await page.getByLabel('Saved anchors').textContent()??'[]');
 expect(annotations[0].anchor.quotedText).toBe('bold text** and *italic text');
 expect(annotations[0].anchor.startOffset).toBe(new TextEncoder().encode('# Component showcase\r\n\r\n😀 Review **').length);
 await expect(reader.locator('mark')).toHaveCount(3);
 await expect(reader.getByRole('button',{name:'Open annotation 1',exact:true})).toHaveText('1');
 await expect(page.getByRole('article',{name:'Annotation 1',exact:true})).toContainText('Clarify both parts');
 await page.getByRole('button',{name:'Edit annotation 1',exact:true}).click();
 await page.getByLabel('Comment on selected text').fill('Updated note');
 await page.getByLabel('Comment on selected text').press('Enter');
 await expect(page.getByRole('dialog')).toHaveCount(0);
 await expect(page.getByRole('article',{name:'Annotation 1',exact:true})).toContainText('Updated note');
 await page.screenshot({path:'out/rich-components.png',fullPage:true});
 await page.getByRole('button',{name:'Remove annotation 1',exact:true}).click();
 await expect(page.getByLabel('Saved anchors')).toHaveText('[]');
 await expect(reader.locator('mark')).toHaveCount(0);
 await expect(reader.getByRole('button',{name:'Open annotation 1',exact:true})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Remove annotation 1',exact:true})).toHaveCount(0);
});

for (const width of [1440, 1000, 600]) {
 test('document scrolls independently at width '+width,async({page})=>{
  await page.setViewportSize({width,height:850});
  await page.goto('/fixtures/rich-document.html');
  const surface=page.locator('.document-surface');
  await expect(surface.getByRole('heading',{name:'Component showcase'})).toBeVisible();
  for (const format of ['Formatted','Raw']) {
   await page.getByRole('button',{name:format,exact:true}).click();
   const before=await surface.evaluate(el=>({height:el.clientHeight,content:el.scrollHeight,top:el.scrollTop}));
   expect(before.content).toBeGreaterThan(before.height);
   await surface.hover();await page.mouse.wheel(0,500);
   await expect.poll(()=>surface.evaluate(el=>el.scrollTop)).toBeGreaterThan(before.top);
   await surface.evaluate(el=>{el.scrollTop=el.scrollHeight});
   expect(await surface.evaluate(el=>Math.abs(el.scrollHeight-el.clientHeight-el.scrollTop))).toBeLessThan(2);
   await surface.evaluate(el=>{el.scrollTop=0});
  }
 });
}
