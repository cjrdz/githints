import { Component, input } from "@angular/core";

@Component({
  selector: "app-user-card",
  template: `<p>{{ name() }}</p>`,
  imports: [],
})
export class UserCard {
  name = input("");
}
