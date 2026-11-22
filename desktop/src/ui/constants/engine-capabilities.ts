// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { EngineType } from '@/shared/types/engines'
import type { EngineCaps } from '@/ui/types/engine-manifest'

export const EngineCapabilities: Record<EngineType, EngineCaps> = {
    ollama: {
        // Ollama has no model keep-alive/expiry UI; unload uses unload_model
        // (POST /api/generate with keep_alive: 0). See docs/services-parity.md#models.
        hasExpiry: false,
        // Ollama unloads via POST /api/generate with keep_alive: 0 (unload_model).
        hasEject: true,
        hasInstall: ['win32', 'darwin', 'linux'],
        hasEnginePort: true,
        hasInstallPath: false,
        hasProxyWebUI: false,
        hasPreferredNode: false,
        hasCrashAlert: false,
        hasModelSearchOnlyWhenRunning: true,
        hasDeleteModel: true,
        engineHub: { label: 'Ollama', url: 'https://ollama.com/library' }
    },
    'lm-studio': {
        hasExpiry: false,
        // LM Studio's `unload_model` action (`lms unload`) is a real eject
        // path. The backend reports which models are loaded in memory, so
        // ModelRow.tsx offers Eject only for a model whose `status` is
        // `'loaded'`.
        hasEject: true,
        hasInstall: ['win32', 'darwin', 'linux'],
        hasEnginePort: true,
        hasInstallPath: false,
        hasProxyWebUI: false,
        hasPreferredNode: false,
        hasCrashAlert: false,
        hasModelSearchOnlyWhenRunning: true,
        hasDeleteModel: true,
        // LM Studio answers /v1/models from an index it builds at startup and
        // exposes no rescan, so nvpair-engine-manager's delete_model restarts the
        // server. Deleting therefore interrupts inference and needs a warning.
        restartsOnModelDelete: true,
        engineHub: { label: 'LM Studio', url: 'https://lmstudio.ai/models' }
    },
    'llama-cpp': {
        hasExpiry: false,
        hasEject: true,
        hasInstall: ['win32', 'darwin', 'linux'],
        hasEnginePort: true,
        hasInstallPath: false,
        hasProxyWebUI: false,
        hasPreferredNode: false,
        hasCrashAlert: false,
        hasModelSearchOnlyWhenRunning: true,
        // The router removes cached models in-process via DELETE /models.
        hasDeleteModel: true,
        engineHub: {
            label: 'llama.cpp',
            url: 'https://huggingface.co/models?library=gguf'
        }
    },
    vllm: {
        hasExpiry: false,
        // vLLM serves one model per process and cannot drop it while running, so
        // Eject stops the engine, which frees all of its GPU memory. Load starts
        // it again on the chosen model.
        hasEject: true,
        // Linux (NVIDIA) and Apple Silicon macOS (vLLM-Metal). vLLM has no
        // native Windows path.
        hasInstall: ['linux', 'darwin'],
        hasEnginePort: true,
        hasInstallPath: false,
        hasProxyWebUI: false,
        hasPreferredNode: false,
        hasCrashAlert: false,
        // Enabled vLLM can browse its disk cache before a serving model is selected.
        hasModelSearchOnlyWhenRunning: false,
        // delete_model removes a model from the shared Hugging Face cache. The
        // loaded model is refused, so Delete is disabled on it until Eject.
        // Deleting the model an ejected or stopped vLLM is set to serve leaves it
        // with no model selected until Load picks one.
        hasDeleteModel: true,
        deleteBlockedWhileLoaded: true,
        // Load writes the model into the launch arguments and restarts vLLM on it.
        loadRestartsEngine: true,
        engineHub: {
            label: 'vLLM',
            url: 'https://huggingface.co/models?pipeline_tag=text-generation&library=safetensors&sort=trending'
        }
    },
    strata: {
        hasExpiry: false,
        // Strata serves one preloaded model per instance; its manifest maps
        // unload_model to stop_engine, so Eject stops the engine like vLLM's.
        hasEject: true,
        // Detect-only manifest: PAIR never installs Strata, so no platform.
        hasInstall: [],
        // The engine's listener comes from Strata's own launch profile;
        // PAIR edits no launch flags.
        hasEnginePort: false,
        hasInstallPath: false,
        hasProxyWebUI: false,
        hasPreferredNode: false,
        hasCrashAlert: false,
        // Inventory is the single loaded model, read from the running /v1/models.
        hasModelSearchOnlyWhenRunning: true,
        hasDeleteModel: false,
        engineHub: {
            label: 'Strata',
            url: 'https://github.com/Gao-Ming-085/Personal-AI-Router'
        }
    }
}
