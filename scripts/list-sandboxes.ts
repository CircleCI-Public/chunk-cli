#!/usr/bin/env bun

const CIRCLECI_TOKEN = process.env.CIRCLECI_TOKEN;

if (!CIRCLECI_TOKEN) {
  console.error('Error: CIRCLECI_TOKEN environment variable is required');
  process.exit(1);
}

async function listSandboxesForOrg(orgId: string, token: string) {
  const response = await fetch(`https://circleci.com/api/v2/sandboxes?org_id=${orgId}`, {
    headers: {
      'Circle-Token': token,
      'Accept': 'application/json',
    },
  });

  if (!response.ok) {
    console.error(`Request failed: ${response.status} ${response.statusText}`);
    const body = await response.text();
    if (body) console.error(body);
    process.exit(1);
  }

  return response.json();
}

async function createSandbox(organizationId: string, name: string, token: string, image?: string) {
  const response = await fetch('https://circleci.com/api/v2/sandboxes', {
    method: 'POST',
    headers: {
      'Circle-Token': token,
      'Accept': 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ organization_id: organizationId, name, ...(image && { image }) }),
  });

  if (!response.ok) {
    console.error(`Request failed: ${response.status} ${response.statusText}`);
    const body = await response.text();
    if (body) console.error(body);
    process.exit(1);
  }

  return response.json();
}

async function createSandboxAccessToken(sandboxId: string, organizationId: string, token: string) {
  const response = await fetch(`https://circleci.com/api/v2/sandboxes/${sandboxId}/access_token`, {
    method: 'POST',
    headers: {
      'Circle-Token': token,
      'Accept': 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ organization_id: organizationId }),
  });

  if (!response.ok) {
    console.error(`Request failed: ${response.status} ${response.statusText}`);
    const body = await response.text();
    if (body) console.error(body);
    process.exit(1);
  }

  return response.json();
}

async function execCommand(command: string, args: string[], accessToken: string) {
  const response = await fetch(`https://circleci.com/api/v2/sandboxes/exec`, {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${accessToken}`,
      'Accept': 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ command: command, args: args }),
  });

  if (!response.ok) {
    console.error(`Request failed: ${response.status} ${response.statusText}`);
    const body = await response.text();
    if (body) console.error(body);
    process.exit(1);
  }

  return response.json();
}
