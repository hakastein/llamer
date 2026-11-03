export class Refusal extends Error {
  constructor(message) {
    super(message);
    this.name = 'Refusal';
  }
}
