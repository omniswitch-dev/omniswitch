import OpenAI from "openai";

export interface SentinelOptions extends OpenAI.ClientOptions {
  /** Base URL of the Sentinel gateway, with or without a trailing /v1. */
  gatewayUrl?: string;
  /** Force routing to a named Sentinel provider. */
  provider?: string;
  /** Trace ID used to group requests in Sentinel observability. */
  traceId?: string;
  /** Session ID used to group requests in Sentinel observability. */
  sessionId?: string;
  /** Provider used for an asynchronous shadow comparison. */
  shadowProvider?: string;
}

export interface SentinelModel {
  id: string;
  owned_by: string;
}

export class Sentinel extends OpenAI {
  constructor(options?: SentinelOptions);
  withTrace(traceId: string, sessionId?: string): Sentinel;
}

export function chat(
  model: string,
  messages: Array<{ role: string; content: unknown; [key: string]: unknown }>,
  options?: SentinelOptions,
): Promise<unknown>;

export function listModels(gatewayUrl?: string): Promise<SentinelModel[]>;

export default Sentinel;
