import { readFile } from 'node:fs/promises';
import { XClient } from './x-client.mjs';

export const username = 'fixture_account';
export const ownId = '100000000000000001';
export const jsonFixture = async (name) => JSON.parse(await readFile(new URL(`../testdata/contract/${name}.json`, import.meta.url), 'utf8'));
export const bundle = '"Rb",0,()=>"Bearer FakePublicToken";' + ['CreateTweet', 'DeleteTweet', 'UserTweets'].map((name) => `queryId:"fixture-${name}",operationName:"${name}",operationType:"${name === 'UserTweets' ? 'query' : 'mutation'}",metadata:{featureSwitches:["enabled","disabled","missing"],fieldToggles:[]}`).join(';');
export function home(handle = username, hash = 'fixture') {
  const state = { session: { user_id: ownId }, entities: { users: { entities: { [ownId]: { screen_name: handle } } } }, featureSwitch: { defaultConfig: { enabled: { value: true }, disabled: { value: true } }, user: { config: { disabled: { value: false } } } } };
  return `<script>window.__INITIAL_STATE__=${JSON.stringify(state)};window.__META_DATA__={};</script><script src="https://abs.twimg.com/responsive-web/client-web/main.${hash}.js"></script>`;
}
export const reply = (data, status = 200, headers = {}) => new Response(data === undefined ? null : typeof data === 'string' ? data : JSON.stringify(data), { status, headers });
export function httpFixture(handler = () => undefined, options = {}) {
  const calls = [];
  let media = 200;
  const client = new XClient({ username, authToken: 'fixture-secret', now: () => Date.parse('2026-10-02T15:00:00Z'), wait: async () => {}, ...options, fetchImpl: async (input, init) => {
    const url = new URL(input);
    calls.push({ url, ...init });
    const custom = await handler(url, init, calls);
    if (custom) return custom;
    if (url.pathname === '/home') return reply(home(), 200, { 'set-cookie': 'ct0=fixture-csrf; Domain=.x.com; Path=/; Secure; HttpOnly' });
    if (url.hostname === 'abs.twimg.com') return reply(bundle);
    if (url.pathname.endsWith('/CreateTweet')) return reply(await jsonFixture('create-tweet-with-id'));
    if (url.pathname.endsWith('/DeleteTweet')) return reply(await jsonFixture('delete-tweet'));
    if (url.pathname.endsWith('/UserTweets')) return reply(await jsonFixture(JSON.parse(url.searchParams.get('variables')).cursor ? 'user-tweets-page-2' : 'user-tweets-page-1'));
    if (url.pathname.endsWith('/subtitles/create.json')) return reply(undefined, 204);
    const command = url.searchParams.get('command');
    if (command === 'INIT') return reply({ media_id_string: String(++media), media_key: `13_${media}` });
    if (command === 'APPEND') return reply(undefined, 204);
    if (command === 'FINALIZE' || command === 'STATUS') return reply({ media_id_string: url.searchParams.get('media_id') });
    throw new Error(`Unexpected request ${url.pathname}`);
  } });
  return { client, calls };
}
