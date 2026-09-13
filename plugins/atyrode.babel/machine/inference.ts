/*
  THE BROKERED INFERENCE BINDING of the machine half (ADR 0038).

  A governed job never holds a model credential. The manifest binds `INFERENCE_SERVICE` to
  `explore` and `evaluate`; the machine owner materializes that binding into one input file as
  `{url, bearer}` — a loopback listener the owner opened for this job and a bearer minted for
  this job alone — and `code engine --brokered <path>` speaks OpenAI-compatible HTTP to
  `${url}/v1` with that bearer. The owner resolves the provider credential, meters every call
  against the job's `limits.inference`, and refuses at the ceiling. Nothing in the sandbox holds
  a provider token, so there is nothing here to scrub.

  THIS MODULE READS THE FILE AND THEN FORGETS IT. The path is what reaches argv; the bearer is
  read once, checked for shape, and dropped — it never enters argv, the environment, a log line,
  a receipt or a failure message. What the check buys is that a run without the binding fails
  HERE, before a prompt is written, with a sentence naming the missing binding, rather than
  inside Code as a connection refused — and above all that it never falls back to anything: a
  job whose manifest did not bind the service has no other lane to a model and must not find one.
*/

import { z } from "zod";
import { INFERENCE_ENDPOINT_FILE, INFERENCE_SERVICE } from "../contract.ts";

/**
 * The endpoint the owner materialized for this job's binding. Loopback and a capability: a
 * document naming anything else is refused rather than followed, because the only writer of
 * this file is the owner's own job runner.
 */
export const InferenceEndpointSchema = z.strictObject({
  url: z.string().regex(/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/),
  bearer: z.string().regex(/^[A-Za-z0-9_-]{32,128}$/),
});

/** Why a run cannot reach a model. The message names the binding and never a value. */
export class InferenceBindingError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "InferenceBindingError";
  }
}

/**
 * Checks that the brokered lane is there and returns the PATH the engine is launched with.
 *
 * The value is deliberately not returned. A caller that received the bearer would be a second
 * place it could be logged, and the engine reads the file itself.
 */
export async function brokeredEndpoint(path: string = INFERENCE_ENDPOINT_FILE): Promise<string> {
  let document: unknown;
  try {
    document = await Bun.file(path).json();
  } catch {
    throw new InferenceBindingError(
      `the job bound no ${INFERENCE_SERVICE.serviceId} service at ${path}, so this run has no ` +
        `lane to a model; bind the service in the operation and install the owner's policy`,
    );
  }
  if (!InferenceEndpointSchema.safeParse(document).success) {
    throw new InferenceBindingError(`${path} is not a ${INFERENCE_SERVICE.serviceId} binding`);
  }
  return path;
}
