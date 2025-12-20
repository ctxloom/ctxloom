# Firestore database for fragment storage

resource "google_firestore_database" "main" {
  provider = google-beta

  project     = var.project_id
  name        = "(default)"
  location_id = var.firestore_location
  type        = "FIRESTORE_NATIVE"

  # Prevent accidental deletion
  deletion_policy = "DELETE"

  depends_on = [google_project_service.apis]
}

# Firestore indexes for efficient queries
resource "google_firestore_index" "fragments_author_name" {
  provider = google-beta

  project    = var.project_id
  database   = google_firestore_database.main.name
  collection = "fragments"

  fields {
    field_path = "author"
    order      = "ASCENDING"
  }

  fields {
    field_path = "name"
    order      = "ASCENDING"
  }

  fields {
    field_path = "updated_at"
    order      = "DESCENDING"
  }
}

resource "google_firestore_index" "fragments_popularity" {
  provider = google-beta

  project    = var.project_id
  database   = google_firestore_database.main.name
  collection = "fragments"

  fields {
    field_path = "downloads"
    order      = "DESCENDING"
  }

  fields {
    field_path = "likes"
    order      = "DESCENDING"
  }
}

resource "google_firestore_index" "personas_author_name" {
  provider = google-beta

  project    = var.project_id
  database   = google_firestore_database.main.name
  collection = "personas"

  fields {
    field_path = "author"
    order      = "ASCENDING"
  }

  fields {
    field_path = "name"
    order      = "ASCENDING"
  }

  fields {
    field_path = "updated_at"
    order      = "DESCENDING"
  }
}
