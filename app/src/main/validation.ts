import { z } from 'zod'

/**
 * Every IPC payload from the renderer is validated here. The renderer is
 * treated as untrusted: it can only reach allow-listed API paths and cannot
 * choose arbitrary files except through OS dialogs or drag-and-drop.
 */

const API_PATH = /^\/v1\/(status|buckets(\/[a-z0-9-]{3,63}(\/(objects|inspect|repair)(\/[^?#]*)?)?)?(\?[\w=&%.\-~+]*)?|maintenance\/(scrub|anti-entropy)|audit(\?limit=\d{1,3})?|invites|chaos\/(corrupt|drop|partition|heal))$/

export const apiRequestSchema = z.object({
  node: z.number().int().min(0).max(1).optional(),
  method: z.enum(['GET', 'PUT', 'POST', 'DELETE']),
  path: z.string().max(2048).regex(API_PATH, 'path not allowed'),
  body: z.unknown().optional()
})

export const setupSchema = z
  .object({
    mode: z.enum(['create', 'join']),
    zone: z
      .string()
      .min(1)
      .max(40)
      .regex(/^[a-zA-Z0-9-]+$/, 'Use letters, numbers and hyphens only'),
    inviteCode: z.string().max(4096).optional(),
    maxGiB: z.number().min(0).max(100_000),
    demoMode: z.boolean(),
    advertise: z
      .string()
      .max(64)
      .regex(/^$|^(\d{1,3}\.){3}\d{1,3}$/, 'Enter an IPv4 address or leave blank')
      .optional()
  })
  .refine((s) => s.mode === 'create' || (s.inviteCode ?? '').trim().startsWith('vault1.'), {
    message: 'Paste the invite code from the other computer (it starts with "vault1.")',
    path: ['inviteCode']
  })

const metaKey = z.string().regex(/^[a-z0-9_-]{1,64}$/)

export const uploadSchema = z.object({
  bucket: z.string().regex(/^[a-z0-9-]{3,63}$/),
  key: z.string().min(1).max(1024).optional(),
  filePath: z.string().max(4096).optional(),
  metadata: z.record(metaKey, z.string().max(512)).refine((m) => Object.keys(m).length <= 32)
})

export const objectRefSchema = z.object({
  bucket: z.string().regex(/^[a-z0-9-]{3,63}$/),
  key: z.string().min(1).max(1024)
})

export const nodeIndexSchema = z.object({ index: z.number().int().min(0).max(1) })

export const textSchema = z.object({ text: z.string().max(8192) })
