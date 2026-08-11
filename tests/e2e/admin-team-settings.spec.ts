import { expect, test, type BrowserContext } from "@playwright/test";

import { expectNamedInteractiveControls, expectNoHorizontalOverflow, preferReducedMotion } from "./helpers";

const adminURL = process.env.ADMIN_MANAGEMENT_E2E_URL;

async function addCSRFCookie(context:BrowserContext,origin:string,value:string){const secure=new URL(origin).protocol==="https:";await context.addCookies([{name:secure?"__Host-admin_csrf":"admin_csrf_e2e",value,url:origin,secure,sameSite:"Strict"}])}

if(!adminURL){
  test("admin team fixture is available",()=>{test.skip(true,"ADMIN_MANAGEMENT_E2E_URL is not set")});
}else{
  test("high-risk team changes use approval and read-only settings stay read-only",async({context,page})=>{
    await preferReducedMotion(page);
    const origin=new URL(adminURL).origin;const tenantID="10000000-0000-4000-8000-000000000001";const merchantID="10000000-0000-4000-8000-000000000002";const actorID="10000000-0000-4000-8000-000000000003";const targetID="20000000-0000-4000-8000-000000000002";let approvalBody:Record<string,unknown>|undefined;let approvalKey="";
    await addCSRFCookie(context,origin,"e".repeat(43));
    await page.route("**/admin/v1/session/me",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({user_id:actorID,session_id:"10000000-0000-4000-8000-000000000004",display_name:"Team Operator",email:"operator@example.com",roles:["admin"],permissions:["team:read","team:manage","team:security_request","settings:read"],scopes:[{tenant_id:tenantID,merchant_id:merchantID}],amr:["mfa"]})}));
    await page.route("**/admin/v1/team/roles",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:[{key:"admin",high_risk:false,permissions:["team:read","team:manage","team:security_request","settings:read"]},{key:"owner",high_risk:true,permissions:["team:read"]},{key:"viewer",high_risk:false,permissions:["team:read"]}]})}));
    await page.route("**/admin/v1/team/members?**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:[{id:"20000000-0000-4000-8000-000000000001",email:"operator@example.com",display_name:"Team Operator",status:"active",role_keys:["admin"],joined_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:2},{id:targetID,email:"viewer@example.com",display_name:"Target Viewer",status:"active",role_keys:["viewer"],joined_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:4}]})}));
    await page.route("**/admin/v1/team/invitations?**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:[]})}));
    await page.route("**/admin/v1/team/security-actions?**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:[]})}));
    await page.route("**/admin/v1/team/security-actions",async route=>{approvalBody=route.request().postDataJSON() as Record<string,unknown>;approvalKey=route.request().headers()["idempotency-key"]??"";await route.fulfill({status:202,contentType:"application/json",body:JSON.stringify({id:"30000000-0000-4000-8000-000000000001",...approvalBody,status:"pending_approval",requested_by:actorID,request_reason:"Owner access reviewed",created_at:"2026-08-11T00:00:00Z",expires_at:"2099-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:1})})});
    await page.route("**/admin/v1/project-settings",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({display_name:"Read only store",locale:"en",timezone:"UTC",support_email:"support@example.com",notifications:{payment_succeeded:true,payment_failed:true,weekly_summary:false},allowed_embed_origins:["https://merchant.example"],updated_at:"2026-08-11T00:00:00Z",version:2})}));

    await page.goto(`${origin}/#/team`,{waitUntil:"domcontentloaded"});
    await page.getByRole("button",{name:/Target Viewer/}).click();await page.getByRole("checkbox",{name:/Owner/}).check();await page.getByLabel("Reason").first().fill("Owner access reviewed");await page.getByRole("button",{name:"Request second approval"}).click();
    expect(approvalBody).toMatchObject({operation:"member.roles.replace",target_member_id:targetID,target_version:4,reason:"Owner access reviewed"});expect(approvalKey.length).toBeGreaterThanOrEqual(8);
    await page.goto(`${origin}/#/settings`,{waitUntil:"domcontentloaded"});await expect(page.locator('input[value="Read only store"]')).toBeDisabled();await expect(page.getByRole("button",{name:"Save"})).toHaveCount(0);await expectNamedInteractiveControls(page);await page.setViewportSize({width:390,height:844});await expectNoHorizontalOverflow(page);
  });
}
