import OpenAI from "openai";

export interface OmniSwitchOptions extends OpenAI.ClientOptions {
  /** Gateway URL with or without a trailing /v1. */
  gatewayUrl?: string;
  /** Dynamic config name or ID sent as x-omniswitch-config. */
  config?: string;
  /** Force routing to a named provider or alias. */
  provider?: string;
  /** Trace ID used to group requests in OmniSwitch observability. */
  traceId?: string;
  /** Session ID used to group requests in OmniSwitch observability. */
  sessionId?: string;
  /** Provider used for asynchronous shadow comparison. */
  shadowProvider?: string;
}

export interface OmniSwitchModel {
  id: string;
  owned_by: string;
}

export class OmniSwitch extends OpenAI {
  constructor(options?: OmniSwitchOptions);
  withConfig(config: string): OmniSwitch;
  withTrace(traceId: string, sessionId?: string): OmniSwitch;
  withProvider(provider: string): OmniSwitch;
}

export class Sentinel extends OmniSwitch {}

export function chat(
  model: string,
  messages: Array<{ role: string; content: unknown; [key: string]: unknown }>,
  options?: OmniSwitchOptions,
  requestOptions?: Record<string, unknown>,
): Promise<unknown>;

export function listModels(
  options?: string | OmniSwitchOptions,
): Promise<OmniSwitchModel[]>;

export function normalizeGatewayUrl(gatewayUrl?: string): string;

export function buildHeaders(
  defaultHeaders?: Record<string, string>,
  options?: Pick<
    OmniSwitchOptions,
    "config" | "provider" | "traceId" | "sessionId" | "shadowProvider"
  >,
): Record<string, string>;

export default OmniSwitch;
