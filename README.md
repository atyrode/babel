# Babel

Babel turns archived conversations with coding agents into evidence-backed opportunities to improve the operator's systems, code, tools, processes, and interactions with agents.

It is an analyzer and recommender, not an autonomous actor. Babel reads session archives, runs a versioned cookbook of analyses, and produces findings and reviewable proposals. It does not open issues, edit repositories, rotate credentials, or apply suggested changes.

The first specification is in [SPEC.md](SPEC.md). It is deliberately a discussion document: the project is still defining its boundary and output before choosing an implementation stack.

Running `babel` opens the primary terminal interface. The first vertical slice inventories the encrypted archive without downloading transcript bodies, presents the session metadata the current archive can provide, and explicitly fetches selected OMP sessions before any AI analysis is introduced.

## Core loop

1. Archive agent sessions and retrieve encrypted snapshots through Babel's archive subsystem, deployed and scheduled by `atyrode/dotfiles`.
2. Discover, normalize, hash, and index sessions without modifying their source.
3. Analyze new or explicitly selected material with versioned recipes.
4. Preserve evidence for every conclusion.
5. Consolidate repeated observations into actionable proposals.
6. Let a human accept, reject, defer, or refine each proposal.
7. Feed accepted improvements into the appropriate repository or operating practice outside Babel.

## Design principles

- **Evidence before conclusions.** Every finding cites the source sessions and relevant excerpts.
- **Local and private by default.** Raw transcripts can contain source code, credentials, personal data, and adversarial instructions.
- **Public code, private data.** Babel is public and independently packageable; archives, credentials, indexes, findings, and model inputs remain local or encrypted.
- **Incremental by default.** Reprocess only new or changed source material, or recipes whose version changed.
- **Suggestions, never side effects.** Integrations may render issue drafts, but publishing or applying them is out of scope.
- **The cookbook is a product.** Analysis recipes are versioned, reviewable assets that improve as useful and harmful patterns are learned.
- **Good patterns matter too.** Babel should preserve effective habits, not merely collect failures.
