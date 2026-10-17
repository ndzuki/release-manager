/**
 * `Customer.status` is a free-form string on the wire, and only `active` and
 * `disabled` are states this console can read. `unknown` is the console's own
 * reading of any other value (or of nothing at all) — it is not a server state
 * and must never be rendered as one of the other two.
 */
export type CustomerStatus = 'active' | 'disabled' | 'unknown';

export interface CustomerSummary {
  id: string;
  name: string;
  slug: string;
  status: CustomerStatus;
  version: number;
  createdAt?: string;
}

export interface CustomerFormInput {
  name: string;
  slug: string;
  version: number;
}

export interface CustomerEvent {
  id: string;
  customerId: string;
  eventType: string;
  createdAt?: string;
}

export interface FieldViolation {
  field: string;
  description: string;
}

export interface SaveError {
  code: string;
  message: string;
  fieldViolations?: FieldViolation[];
}
