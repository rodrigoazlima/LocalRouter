import { buildBinary } from './helpers/localrouter';

export default async function globalSetup(): Promise<void> {
  console.log('[setup] building LocalRouter binary...');
  await buildBinary();
  console.log('[setup] binary ready');
}
