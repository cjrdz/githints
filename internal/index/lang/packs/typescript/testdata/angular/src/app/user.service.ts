import { Injectable, inject } from "@angular/core";
import { HttpClient } from "@angular/common/http";

@Injectable({ providedIn: "root" })
export class UserService {
  private http = inject(HttpClient);
  list() {
    return this.http.get("/api/users");
  }
}
