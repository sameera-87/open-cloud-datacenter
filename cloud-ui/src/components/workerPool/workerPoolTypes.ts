/**
 * Shared types, constants, and pure validation helpers for worker pool forms.
 * Kept in a separate .ts file so WorkerPoolForm.tsx can be a pure-components
 * file (satisfying react-refresh/only-export-components).
 */
import type { LabelPair } from './LabelEditor';
import type { NodePoolTaint } from './TaintEditor';

export const POOL_NAME_RE = /^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$/;
export const RESERVED_POOL_NAMES = ['system'] as const;

export const SIZES = [
  { id: 'small', label: 'Small', specs: '2 vCPU · 4 GB RAM · 40 GB disk' },
  { id: 'medium', label: 'Medium', specs: '4 vCPU · 8 GB RAM · 40 GB disk' },
  { id: 'large', label: 'Large', specs: '8 vCPU · 16 GB RAM · 80 GB disk' },
  { id: 'xlarge', label: 'X-Large', specs: '16 vCPU · 32 GB RAM · 160 GB disk' },
] as const;

export type PoolSize = (typeof SIZES)[number]['id'];

export interface WorkerPoolFormValue {
  name: string;
  size: PoolSize;
  count: number;
  diskOverride: boolean;
  diskGb: number;
  taints: NodePoolTaint[];
  labels: LabelPair[];
}

export function defaultWorkerPoolFormValue(): WorkerPoolFormValue {
  return {
    name: '',
    size: 'large',
    count: 3,
    diskOverride: false,
    diskGb: 80,
    taints: [],
    labels: [],
  };
}

/** Returns a map of field name -> error string for any invalid fields. */
export function validateWorkerPoolForm(
  value: WorkerPoolFormValue,
  existingNames: string[],
): Partial<Record<keyof WorkerPoolFormValue, string>> {
  const errors: Partial<Record<keyof WorkerPoolFormValue, string>> = {};

  if (!value.name) {
    errors.name = 'Name is required.';
  } else if (RESERVED_POOL_NAMES.includes(value.name as 'system')) {
    errors.name = '"system" is reserved.';
  } else if (existingNames.includes(value.name)) {
    errors.name = 'A pool with this name already exists.';
  } else if (!POOL_NAME_RE.test(value.name)) {
    errors.name =
      'Use lowercase letters, numbers, hyphens. Must start with a letter, max 40 chars.';
  }

  if (value.count < 1 || value.count > 50) {
    errors.count = 'Count must be between 1 and 50.';
  }

  return errors;
}

/** Returns true when the form has no validation errors. */
export function workerPoolFormIsValid(
  value: WorkerPoolFormValue,
  existingNames: string[],
): boolean {
  return Object.keys(validateWorkerPoolForm(value, existingNames)).length === 0;
}
