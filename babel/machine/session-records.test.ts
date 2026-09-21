import { expect, test } from "bun:test";

import { secretScan, type SecretScan } from "./preflight.ts";
import { recordReader, sessionDigester } from "./session-records.ts";

const LIMIT = 4 << 20;
const encoder = new TextEncoder();

function hash(bytes: Uint8Array | string): string {
  return `sha256:${new Bun.CryptoHasher("sha256").update(bytes).digest("hex")}`;
}

function feed(
  writer: { write(chunk: Uint8Array): void },
  bytes: Uint8Array,
  cuts: readonly number[],
): void {
  let start = 0;
  for (const end of cuts) {
    writer.write(bytes.subarray(start, end));
    start = end;
  }
  writer.write(bytes.subarray(start));
}

function records(bytes: Uint8Array, cuts: readonly number[] = []): [string, number][] {
  const out: [string, number][] = [];
  const reader = recordReader((record, line) => out.push([record, line]));
  feed(reader, bytes, cuts);
  reader.finish();
  return out;
}

function digest(bytes: Uint8Array, cuts: readonly number[] = [], scan?: SecretScan) {
  const sealed: string[] = [];
  const reader = sessionDigester(
    {
      write: (chunk) => {
        sealed.push(typeof chunk === "string" ? chunk : new TextDecoder().decode(chunk));
      },
      close: async () => {},
    },
    scan,
  );
  feed(reader, bytes, cuts);
  const measured = reader.finish();
  return { measured, body: sealed.join("") };
}

test("byte boundaries preserve canonical records, opaque evidence and emitted line numbers", () => {
  const bytes = encoder.encode(
    '\ufeff\n \t\r\n {"z":[{"b":2,"a":"😀é"}],"a":true}\r\nnot-json\r\nnull\n{"torn":',
  );
  const expected: [string, number][] = [
    ['{"a":true,"z":[{"a":"😀é","b":2}]}\n', 1],
    ["!not-json\r\n", 2],
    ["null\n", 3],
    ['!{"torn":\n', 4],
  ];
  for (let cut = 0; cut <= bytes.length; cut++) {
    expect(records(bytes, [cut])).toEqual(expected);
  }
  const cuts = Array.from({ length: bytes.length }, (_, index) => index + 1);
  expect(records(bytes, cuts)).toEqual(expected);
  const { measured, body } = digest(bytes, cuts);
  expect(body).toBe(expected.map(([record]) => record).join(""));
  expect(measured).toEqual({
    captureDigest: hash(bytes),
    sourceDigest: hash(encoder.encode(body)),
    bytes: bytes.length,
    records: 4,
  });
});

test("a newline arriving with an oversized chunk cannot bypass fixed record segmentation", () => {
  const long = "x".repeat(LIMIT * 2 + 17);
  const bytes = encoder.encode(`${long}\n{}\n`);
  const expected: [string, number][] = [
    [`!${"x".repeat(LIMIT)}\n`, 1],
    [`!${"x".repeat(LIMIT)}\n`, 2],
    [`!${"x".repeat(17)}\n`, 3],
    ["{}\n", 4],
  ];
  const body = expected.map(([record]) => record).join("");
  for (const cuts of [[], [LIMIT], [LIMIT + 1, LIMIT * 2 + 17]]) {
    expect(records(bytes, cuts)).toEqual(expected);
    expect(digest(bytes, cuts)).toEqual({
      measured: {
        captureDigest: hash(bytes),
        sourceDigest: hash(body),
        bytes: bytes.length,
        records: 4,
      },
      body,
    });
  }
});

test("a complete JSON record exactly at the limit stays whole", () => {
  const record = `"${"a".repeat(LIMIT - 2)}"`;
  const bytes = encoder.encode(`${record}\nfalse`);
  const expected: [string, number][] = [[`${record}\n`, 1], ["false\n", 2]];
  expect(records(bytes)).toEqual(expected);
  expect(records(bytes, [LIMIT - 1, LIMIT, LIMIT + 1])).toEqual(expected);
});

test("size segmentation preserves a Unicode pair even when its UTF-8 bytes arrive separately", () => {
  const prefix = "x".repeat(LIMIT - 1);
  const bytes = encoder.encode(`${prefix}😀tail\n`);
  const expected: [string, number][] = [[`!${prefix}\n`, 1], ["!😀tail\n", 2]];
  for (const cuts of [[], [LIMIT - 1, LIMIT, LIMIT + 1, LIMIT + 2, LIMIT + 3]]) {
    expect(records(bytes, cuts)).toEqual(expected);
    const { measured, body } = digest(bytes, cuts);
    expect(body).toBe(expected.map(([record]) => record).join(""));
    expect(measured.sourceDigest).toBe(hash(encoder.encode(body)));
    expect(measured.records).toBe(2);
  }
});

test("decoder flush applies the size rule to an incomplete final UTF-8 sequence", () => {
  const bytes = new Uint8Array(LIMIT + 2);
  bytes.fill(0x78, 0, LIMIT);
  bytes.set([0xf0, 0x9f], LIMIT);
  const expected: [string, number][] = [[`!${"x".repeat(LIMIT)}\n`, 1], ["!\ufffd\n", 2]];
  expect(records(bytes)).toEqual(expected);
  expect(records(bytes, [LIMIT, LIMIT + 1])).toEqual(expected);
});

test("scanning hashes the sealed redacted bytes and keeps locators independent of chunking", () => {
  const key = `${"AKIA"}IOSFODNN7SYNTH01`;
  const bytes = encoder.encode(`\n{"z":"😀","a":"${key}"}\n\n`);
  const raw = digest(bytes);
  const expectedBody = '{"a":"[[babel-redacted:aws-access-key-id@1:6+20]]","z":"😀"}\n';
  for (const cuts of [[], Array.from({ length: bytes.length }, (_, index) => index + 1)]) {
    const scan = secretScan();
    const { measured, body } = digest(bytes, cuts, scan);
    expect(body).toBe(expectedBody);
    expect(measured.sourceDigest).toBe(hash(encoder.encode(body)));
    expect(measured.sourceDigest).not.toBe(raw.measured.sourceDigest);
    expect(measured.captureDigest).toBe(hash(bytes));
    expect(measured.records).toBe(1);
    expect(scan.report().sites).toEqual([
      { class: "aws-access-key-id", line: 1, offset: 6, length: 20 },
    ]);
  }
});

test("an empty or blank capture has no records but still captures every byte", () => {
  for (const bytes of [new Uint8Array(), encoder.encode("\n\r\n \t")]) {
    expect(digest(bytes)).toEqual({
      body: "",
      measured: {
        captureDigest: hash(bytes),
        sourceDigest: hash(""),
        bytes: bytes.length,
        records: 0,
      },
    });
  }
});
