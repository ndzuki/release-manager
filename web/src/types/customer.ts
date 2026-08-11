export type CustomerStatus = 'active' | 'disabled';

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
